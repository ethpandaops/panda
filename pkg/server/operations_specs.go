package server

import (
	"net/http"
	"strings"

	"github.com/ethpandaops/panda/pkg/operations"
)

func (s *service) handleSpecsOperation(
	operationID string,
	w http.ResponseWriter,
	r *http.Request,
) bool {
	switch operationID {
	case "specs.get_constant":
		s.handleSpecsGetConstant(w, r)
	case "specs.list_constants":
		s.handleSpecsListConstants(w, r)
	case "specs.get_spec":
		s.handleSpecsGetSpec(w, r)
	default:
		return false
	}

	return true
}

func (s *service) handleSpecsGetConstant(w http.ResponseWriter, r *http.Request) {
	if s.specsRegistry == nil {
		http.Error(w, "consensus specs not available", http.StatusServiceUnavailable)
		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	name, err := requiredStringArg(req.Args, "name")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fork := optionalStringArg(req.Args, "fork")

	constant, found := s.specsRegistry.GetConstant(name, fork)
	if !found {
		http.Error(w, "constant not found: "+name, http.StatusNotFound)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "object",
		Data: map[string]any{
			"name":  constant.Name,
			"value": constant.Value,
			"fork":  constant.Fork,
		},
	})
}

func (s *service) handleSpecsListConstants(w http.ResponseWriter, r *http.Request) {
	if s.specsRegistry == nil {
		http.Error(w, "consensus specs not available", http.StatusServiceUnavailable)
		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fork := optionalStringArg(req.Args, "fork")
	prefix := strings.ToUpper(optionalStringArg(req.Args, "prefix"))

	allConstants := s.specsRegistry.AllConstants()
	results := make([]map[string]string, 0, len(allConstants))

	for _, c := range allConstants {
		if fork != "" && c.Fork != fork {
			continue
		}

		if prefix != "" && !strings.HasPrefix(strings.ToUpper(c.Name), prefix) {
			continue
		}

		results = append(results, map[string]string{
			"name":  c.Name,
			"value": c.Value,
			"fork":  c.Fork,
		})
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "object",
		Data: map[string]any{
			"constants": results,
			"count":     len(results),
		},
	})
}

func (s *service) handleSpecsGetSpec(w http.ResponseWriter, r *http.Request) {
	if s.specsRegistry == nil {
		http.Error(w, "consensus specs not available", http.StatusServiceUnavailable)
		return
	}

	req, err := decodeOperationRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fork, err := requiredStringArg(req.Args, "fork")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	topic, err := requiredStringArg(req.Args, "topic")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	spec, found := s.specsRegistry.GetSpec(fork, topic)
	if !found {
		http.Error(w, "spec not found: "+fork+"/"+topic, http.StatusNotFound)
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "object",
		Data: map[string]any{
			"fork":    spec.Fork,
			"topic":   spec.Topic,
			"title":   spec.Title,
			"content": spec.Content,
			"url":     spec.URL,
		},
	})
}
