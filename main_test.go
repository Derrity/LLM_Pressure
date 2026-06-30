package main

import (
	"reflect"
	"testing"
)

func TestPreprocessListModelArgsDetectsBareModelFlag(t *testing.T) {
	tests := []struct {
		name     string
		in       []string
		wantArgs []string
		wantList bool
	}{
		{
			name:     "bare short model flag at end",
			in:       []string{"-p", "example-provider", "-m"},
			wantArgs: []string{"-p", "example-provider"},
			wantList: true,
		},
		{
			name:     "bare short model flag before another flag",
			in:       []string{"-u", "https://api.example.com/v1", "-m", "-n", "32"},
			wantArgs: []string{"-u", "https://api.example.com/v1", "-n", "32"},
			wantList: true,
		},
		{
			name:     "bare long model flag",
			in:       []string{"-profile", "example-provider", "-model"},
			wantArgs: []string{"-profile", "example-provider"},
			wantList: true,
		},
		{
			name:     "empty equals model flag",
			in:       []string{"-p", "example-provider", "-m="},
			wantArgs: []string{"-p", "example-provider"},
			wantList: true,
		},
		{
			name:     "empty model flag value",
			in:       []string{"-p", "example-provider", "-m", ""},
			wantArgs: []string{"-p", "example-provider"},
			wantList: true,
		},
		{
			name:     "short model flag with value",
			in:       []string{"-p", "example-provider", "-m", "glm-5.2"},
			wantArgs: []string{"-p", "example-provider", "-m", "glm-5.2"},
			wantList: false,
		},
		{
			name:     "equals model flag with value",
			in:       []string{"-p", "example-provider", "-m=glm-5.2"},
			wantArgs: []string{"-p", "example-provider", "-m=glm-5.2"},
			wantList: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotList := preprocessListModelArgs(tt.in)
			if gotList != tt.wantList {
				t.Fatalf("preprocessListModelArgs() list = %v, want %v", gotList, tt.wantList)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("preprocessListModelArgs() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}
