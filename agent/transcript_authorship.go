package agent

import (
	"primeradiant.com/evener/agent/activitybound"
	"primeradiant.com/evener/agent/schema"
)

type internalTranscriptInputKey struct{}

func humanTranscriptAuthorship() *schema.TranscriptAuthorship {
	return &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorHuman}}
}

func inheritedTranscriptAuthorship(a *schema.TranscriptAuthorship) *schema.TranscriptAuthorship {
	cloned := a.Clone()
	if cloned == nil {
		cloned = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}}
	}
	cloned.Inherited = true
	return cloned
}

// Call before session/transcript leaf locks. The descriptor's physical store
// owner is not necessarily the delegate's logical parent.
func (s *Session) transcriptAuthor() schema.TranscriptAuthor {
	if s.delegateController != nil && s.owningDelegateID != "" {
		c := s.delegateController
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.transcriptDelegateAuthorLocked(s.owningDelegateID)
	}
	if s.id == "" {
		return schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}
	}
	return schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: s.id}
}

func (c *delegateTreeController) transcriptDelegateAuthorLocked(id string) schema.TranscriptAuthor {
	unknown := schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}
	a := c.durable[id]
	if a == nil {
		return unknown
	}
	d := a.Descriptor
	owner := c.rootSessionID
	if d.ParentDelegateID != "" {
		parent := c.durable[d.ParentDelegateID]
		if parent == nil {
			return unknown
		}
		owner = parent.Descriptor.ChildSessionID
	}
	return schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: d.ChildSessionID, OwnerSessionID: owner, DelegateID: id, DelegateName: activitybound.Truncate(d.Name, activitybound.MaxLabelRunes)}
}

func (c *delegateTreeController) transcriptActorAuthorLocked(actor delegateActor) schema.TranscriptAuthor {
	if actor.lease != nil {
		return c.transcriptDelegateAuthorLocked(actor.lease.delegateID)
	}
	return schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: actor.rootSessionID}
}

func (s *Session) roundTranscriptAuthorship() *schema.TranscriptAuthorship {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roundAuthorship.Clone()
}
