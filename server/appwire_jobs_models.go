package server

import (
	"context"

	"primeradiant.com/evener/appwire"
)

func (s *Server) handleAppTasksList(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	s.mu.RLock()
	fn := s.tasksFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.TaskListResponse{}, nil
	}
	return appwire.TaskListResponse{Data: fn()}, nil
}

func (s *Server) handleAppJobsList(_ context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	s.mu.RLock()
	fn := s.jobsFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsListResponse{}, nil
	}
	data, err := fn(params)
	if err != nil {
		return appwire.JobsListResponse{}, err
	}
	return appwire.JobsListResponse{Data: data}, nil
}

func (s *Server) handleAppJobsOutput(_ context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	s.mu.RLock()
	fn := s.jobOutputFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsOutputResponse{}, appwire.Unavailable("job output not available")
	}
	data, found, err := fn(params.JobID, params.BeforeBytes, params.MaxBytes)
	if err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	if !found {
		return appwire.JobsOutputResponse{}, appwire.InvalidParams("job not found: " + params.JobID)
	}
	return appwire.JobsOutputResponse{Data: data}, nil
}

func (s *Server) handleAppJobsGet(_ context.Context, params appwire.JobsGetParams) (appwire.JobsGetResponse, error) {
	s.mu.RLock()
	fn := s.jobGetFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsGetResponse{}, appwire.Unavailable("job not available")
	}
	data, found, err := fn(params.JobID)
	if err != nil {
		return appwire.JobsGetResponse{}, err
	}
	if !found {
		return appwire.JobsGetResponse{}, appwire.InvalidParams("job not found: " + params.JobID)
	}
	return appwire.JobsGetResponse{Data: data}, nil
}

func (s *Server) handleAppModelList(ctx context.Context, _ appwire.ModelListParams) (appwire.ModelListResponse, error) {
	s.mu.RLock()
	fn := s.listModelsFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{}}, nil
	}
	models, err := fn(ctx)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	if models == nil {
		models = []appwire.ModelDescriptor{}
	}
	return appwire.ModelListResponse{Data: models}, nil
}
