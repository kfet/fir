package main

import (
	"log/slog"
	"testing"

	firlog "github.com/kfet/fir/pkg/log"
)

func TestResolveLogLevel(t *testing.T) {
	cases := []struct {
		name    string
		args    *Args
		env     map[string]string
		wantLvl slog.Level
		wantOn  bool
	}{
		{"default off", &Args{}, nil, slog.LevelInfo, false},
		{"FIR_DEBUG=1", &Args{}, map[string]string{"FIR_DEBUG": "1"}, slog.LevelDebug, true},
		{"--debug", &Args{Debug: true}, nil, slog.LevelDebug, true},
		{"-v", &Args{VerboseCount: 1}, nil, slog.LevelDebug, true},
		{"-vv", &Args{VerboseCount: 2}, nil, firlog.LevelTrace, true},
		{"-vvv clamps to trace", &Args{VerboseCount: 5}, nil, firlog.LevelTrace, true},
		{
			"FIR_LOG_LEVEL beats FIR_DEBUG",
			&Args{},
			map[string]string{"FIR_DEBUG": "1", "FIR_LOG_LEVEL": "trace"},
			firlog.LevelTrace, true,
		},
		{
			"CLI -v beats FIR_LOG_LEVEL=info",
			&Args{VerboseCount: 1},
			map[string]string{"FIR_LOG_LEVEL": "info"},
			slog.LevelDebug, true,
		},
		{
			"FIR_LOG_LEVEL=trace alone",
			&Args{},
			map[string]string{"FIR_LOG_LEVEL": "trace"},
			firlog.LevelTrace, true,
		},
		{
			"FIR_LOG_LEVEL garbage falls through to default",
			&Args{},
			map[string]string{"FIR_LOG_LEVEL": "bogus"},
			slog.LevelInfo, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FIR_DEBUG", "")
			t.Setenv("FIR_LOG_LEVEL", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			gotLvl, gotOn := resolveLogLevel(tc.args)
			if gotLvl != tc.wantLvl || gotOn != tc.wantOn {
				t.Errorf("got (%v, %v) want (%v, %v)", gotLvl, gotOn, tc.wantLvl, tc.wantOn)
			}
		})
	}
}
