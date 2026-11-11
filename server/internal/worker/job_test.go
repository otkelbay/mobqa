package worker_test

import (
	"errors"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/worker"
)

func TestEnqueueInputValidate(t *testing.T) {
	valid := worker.EnqueueInput{Kind: "run", Payload: []byte(`{"run_id":"r1"}`), MaxAttempts: 3}
	tests := []struct {
		name    string
		input   worker.EnqueueInput
		wantErr bool
	}{
		{name: "valid", input: valid},
		{name: "delayed", input: worker.EnqueueInput{Kind: "run", Payload: []byte(`{}`), MaxAttempts: 1, Delay: time.Minute}},
		{name: "empty kind", input: worker.EnqueueInput{Payload: []byte(`{}`), MaxAttempts: 1}, wantErr: true},
		{name: "kind with dash", input: worker.EnqueueInput{Kind: "run-test", Payload: []byte(`{}`), MaxAttempts: 1},
			wantErr: true},
		{name: "kind too long", input: worker.EnqueueInput{Kind: "abcdefghijklmnopqrstuvwxyzabcdefghijklmno",
			Payload: []byte(`{}`), MaxAttempts: 1}, wantErr: true},
		{name: "zero attempts", input: worker.EnqueueInput{Kind: "run", Payload: []byte(`{}`)}, wantErr: true},
		{name: "negative delay", input: worker.EnqueueInput{Kind: "run", Payload: []byte(`{}`), MaxAttempts: 1,
			Delay: -time.Second}, wantErr: true},
		{name: "payload not JSON", input: worker.EnqueueInput{Kind: "run", Payload: []byte(`run`), MaxAttempts: 1},
			wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, fault.ErrInvalidInput) {
				t.Fatalf("Validate() = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  worker.Config
		wantErr bool
	}{
		{name: "valid", config: testConfig()},
		{name: "no owner", config: worker.Config{Kind: "run", Concurrency: 1, Lease: time.Minute,
			PollInterval: time.Minute, RetryBase: time.Second, RetryMax: time.Minute}, wantErr: true},
		{name: "no concurrency", config: worker.Config{Kind: "run", Owner: "w", Lease: time.Minute,
			PollInterval: time.Minute, RetryBase: time.Second, RetryMax: time.Minute}, wantErr: true},
		{name: "no lease", config: worker.Config{Kind: "run", Owner: "w", Concurrency: 1,
			PollInterval: time.Minute, RetryBase: time.Second, RetryMax: time.Minute}, wantErr: true},
		{name: "retry max below base", config: worker.Config{Kind: "run", Owner: "w", Concurrency: 1,
			Lease: time.Minute, PollInterval: time.Minute, RetryBase: time.Minute, RetryMax: time.Second}, wantErr: true},
		{name: "bad kind", config: worker.Config{Kind: "Run", Owner: "w", Concurrency: 1, Lease: time.Minute,
			PollInterval: time.Minute, RetryBase: time.Second, RetryMax: time.Minute}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
