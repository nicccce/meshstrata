package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nicccce/meshstrata/controller/internal/releases"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/localstate"
)

// SubmitRelease accepts only format-1 plans from the local Controller operator.
func SubmitRelease(ctx context.Context, cfg config.Controller, input io.Reader) (any, error) {
	if cfg.Secure == nil {
		return nil, errors.New("submit-release requires secure configuration")
	}
	const maxInput = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(input, maxInput+1))
	defer clear(raw)
	if err != nil || len(raw) > maxInput {
		return nil, errors.New("invalid release input")
	}
	var request struct {
		Mutation domain.Mutation `json:"mutation"`
		Plan     releases.Plan   `json:"plan"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid release input")
	}
	unlock, err := localstate.Lock(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	store, err := storage.Open(ctx, cfg.StateDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	services, err := composeSecure(ctx, *cfg.Secure, store, http.NotFoundHandler())
	if err != nil {
		return nil, err
	}
	defer services.channel.Close()
	defer services.releases.Close()
	out, err := services.releases.Submit(ctx, request.Mutation, request.Plan)
	if err != nil {
		var problem *domain.Problem
		if errors.As(err, &problem) {
			return nil, problem
		}
		return nil, errors.New("release submission unavailable")
	}
	return out, nil
}
