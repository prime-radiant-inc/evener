package hub

import (
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/componentlog"
)

// unarchiveProjectsUsedSinceNewSessions clears a project's manual archive
// decision when the navigation snapshot carries a top-level session created
// after that decision. Navigation files an archived project's sessions as
// archived whatever their own decision says, so without this a session
// launched into an archived project is filed as archived at birth.
func unarchiveProjectsUsedSinceNewSessions(archive *hubcore.ArchiveStore, snapshot navigationSnapshot) {
	decisions, err := archive.DecisionStates()
	if err != nil || len(decisions) == 0 {
		return
	}
	roots := hubcore.NewRootIndex(snapshot.metas)
	running := hubcore.RunningSubagentIDs(snapshot.live)
	sessions := make([]hubcore.SessionProject, 0, len(snapshot.metas)+len(snapshot.live))
	seen := make(map[string]bool, len(snapshot.metas))
	for _, meta := range snapshot.metas {
		seen[meta.ID] = true
		project, ok := snapshot.reportedProjects[meta.ID]
		if !ok {
			project, ok = snapshot.projects[hubcore.EffectiveWorkingDir(meta)]
		}
		if ok && project.ID != "" {
			source := hubcore.SessionSourceFromID(meta.ID)
			sessions = append(sessions, sessionProject(source, project.ID, meta, !roots.TopLevel(meta.ID) || running[meta.ID]))
		}
	}
	// A session launched since the last past-index fold is still only in the
	// roster, so read its meta straight from its state dir. When navigation
	// already resolved the entry's project, skip the read unless that project
	// is archived.
	for _, live := range snapshot.live {
		if live.Crashed || live.SessionID == "" || live.StateDir == "" || seen[live.SessionID] {
			continue
		}
		source := hubcore.LiveEntrySource(live)
		projectID := live.Project.ID
		if reported, ok := snapshot.reportedProjects[live.SessionID]; ok {
			projectID = reported.ID
		}
		if projectID != "" {
			if decision, ok := decisions[hubcore.ProjectDecisionKey(source, projectID)]; !ok || !decision.Archived {
				continue
			}
		}
		meta, err := schema.LoadSessionMeta(live.StateDir, live.SessionID)
		if err != nil {
			continue
		}
		if projectID == "" {
			project, ok := snapshot.projects[hubcore.EffectiveWorkingDir(meta)]
			if !ok || project.ID == "" {
				continue
			}
			projectID = project.ID
			if decision, ok := decisions[hubcore.ProjectDecisionKey(source, projectID)]; !ok || !decision.Archived {
				continue
			}
		}
		sessions = append(sessions, sessionProject(source, projectID, meta, meta.IsSubagent || !roots.TopLevel(meta.ID) || running[meta.ID]))
	}
	log := componentlog.New("hub")
	for _, key := range hubcore.ProjectsToUnarchive(decisions, sessions) {
		if _, err := archive.SetIfArchived(key.Source, key.Kind, key.ID, time.Now()); err != nil {
			log.Info("unarchive project", "project", key.ID, "source", key.Source, "err", err)
		}
	}
}

// sessionProject fills the launch rule's inputs for one session.
func sessionProject(source, projectID string, meta schema.SessionMeta, subagent bool) hubcore.SessionProject {
	return hubcore.SessionProject{
		ProjectID: projectID,
		Source:    source,
		CreatedAt: meta.CreatedAt,
		Subagent:  subagent,
	}
}
