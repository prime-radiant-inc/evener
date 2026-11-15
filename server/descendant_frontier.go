package server

import (
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
)

// Entries are immutable. Publication and replacement install new values so a
// callback carrying an earlier cut cannot compare current state with itself.
type descendantFrontierEntry struct {
	sessionID      string
	registration   uint64
	bootGeneration string
	sample         func() events.ExecutionFrontier
	lastInstall    events.ExecutionFrontier
}

func (s *Server) descendantFrontierHooks(ownerID, childID string) agent.TranscriptPublicationHooks {
	var identity uint64 // Read and written only under Server.mu.
	return agent.TranscriptPublicationHooks{
		RegisterFrontier: func(sample func() events.ExecutionFrontier) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.appThreadID != ownerID {
				return
			}
			s.appDescendantFrontierSerial++
			identity = s.appDescendantFrontierSerial
			if s.appDescendantFrontiers == nil {
				s.appDescendantFrontiers = make(map[string]*descendantFrontierEntry)
			}
			s.appDescendantFrontiers[childID] = &descendantFrontierEntry{sessionID: childID, registration: identity, sample: sample}
		},
		PublishFrontier: func(generation string) {
			s.mu.Lock()
			defer s.mu.Unlock()
			entry := s.appDescendantFrontiers[childID]
			if entry == nil || entry.registration != identity {
				return
			}
			next := *entry
			next.bootGeneration = generation
			s.appDescendantFrontiers[childID] = &next
		},
		ReleaseFrontier: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if entry := s.appDescendantFrontiers[childID]; entry != nil && entry.registration == identity {
				delete(s.appDescendantFrontiers, childID)
			}
		},
	}
}

// installDescendantFrontier runs after CommitProjection returns. No server or
// projection lock spans the child's Session.mu and frontier-leaf sample.
func (s *Server) installDescendantFrontier(ownerID string, carried *descendantFrontierEntry) {
	if carried == nil {
		return
	}
	frontier := carried.sample()
	s.mu.Lock()
	defer s.mu.Unlock()
	if projection := s.appDescendants[carried.sessionID]; projection != nil {
		if !s.acceptDescendantFrontierInstallLocked(ownerID, carried, frontier) {
			return
		}
		// Deliberately no notification. Addressed metadata reads see this value,
		// while subscribers keep their last full row until another full read.
		projection.thread.Evener.LatestExecutionTurnID = frontier.TurnID
	}
}

// acceptDescendantFrontierInstallLocked records only successful installs. The
// watermark belongs to the registration, not the row a predecessor left behind.
// Both first-record samples and SessionStart's already-sampled bootstrap use it.
func (s *Server) acceptDescendantFrontierInstallLocked(ownerID string, carried *descendantFrontierEntry, frontier events.ExecutionFrontier) bool {
	current := s.appDescendantFrontiers[carried.sessionID]
	if s.appThreadID != ownerID || current == nil || current.registration != carried.registration || current.bootGeneration != carried.bootGeneration {
		return false
	}
	if frontier.Sequence < current.lastInstall.Sequence {
		return false
	}
	next := *current
	next.lastInstall = frontier
	s.appDescendantFrontiers[carried.sessionID] = &next
	return true
}
