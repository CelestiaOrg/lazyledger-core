package remote

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var eventRequest Event
	err := json.NewDecoder(r.Body).Decode(&eventRequest)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = s.writeEvent(eventRequest)
	if err != nil {
		w.Write(NewErrorResponse(err.Error()))
		return
	}

	s.logger.Info(
		"wrote event",
		"nodeID", eventRequest.NodeID,
		"chainID", eventRequest.ChainID,
		"type", eventRequest.Type,
	)

	w.Write(NewOKResponse())
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var batchRequest BatchRequest
	err := json.NewDecoder(r.Body).Decode(&batchRequest)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = s.writeBatch(batchRequest)
	if err != nil {
		w.Write(NewErrorResponse(err.Error()))
		return
	}

	s.logger.Info(
		"wrote batch",
		"nodeID", batchRequest.NodeID,
		"chainID", batchRequest.ChainID,
		"events", len(batchRequest.Events),
	)

	w.Write(NewOKResponse())
}

func (s *Server) writeEvent(ev Event) error {
	f, err := s.GetFile(ev.FilePath())
	if err != nil {
		return err
	}

	return WriteJsonLinesFile(f, []Event{ev})
}

// todo: sort events by type and write in batches
func (s *Server) writeBatch(br BatchRequest) error {
	for _, ev := range br.Events {
		err := s.writeEvent(ev)
		if err != nil {
			return err
		}
	}

	return nil
}
