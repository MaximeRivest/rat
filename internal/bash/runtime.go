package bash

import "sync"

type inputResponse struct {
	Text      string
	Cancelled bool
}

type RuntimeManager struct {
	cwd     string
	mu      sync.Mutex
	worker  *BashWorker
	pending map[string]chan inputResponse
}

func NewRuntimeManager(cwd string) *RuntimeManager {
	return &RuntimeManager{
		cwd:     cwd,
		pending: make(map[string]chan inputResponse),
	}
}

func (r *RuntimeManager) Worker() (*BashWorker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.worker == nil {
		worker, err := NewBashWorker(r.cwd, nil)
		if err != nil {
			return nil, err
		}
		r.worker = worker
	}

	return r.worker, nil
}

func (r *RuntimeManager) Reset() error {
	worker, err := r.Worker()
	if err != nil {
		return err
	}
	return worker.Reset()
}

func (r *RuntimeManager) RegisterPendingInput(execID string) chan inputResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := make(chan inputResponse, 1)
	r.pending[execID] = ch
	return ch
}

func (r *RuntimeManager) ProvideInput(execID, text string) bool {
	r.mu.Lock()
	ch, ok := r.pending[execID]
	r.mu.Unlock()
	if !ok {
		return false
	}

	select {
	case ch <- inputResponse{Text: text}:
		return true
	default:
		return false
	}
}

func (r *RuntimeManager) CancelInput(execID string) bool {
	r.mu.Lock()
	ch, ok := r.pending[execID]
	r.mu.Unlock()
	if !ok {
		return false
	}

	select {
	case ch <- inputResponse{Cancelled: true}:
		return true
	default:
		return false
	}
}

func (r *RuntimeManager) ClearPendingInput(execID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, execID)
}

func (r *RuntimeManager) Shutdown() error {
	r.mu.Lock()
	worker := r.worker
	r.worker = nil
	r.mu.Unlock()

	if worker != nil {
		return worker.Shutdown()
	}
	return nil
}
