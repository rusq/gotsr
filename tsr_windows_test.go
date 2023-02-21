package gotsr

import (
	"testing"
	"time"
)

func Test_stageInit(t *testing.T) {
	type args struct {
		pidFile string
		vars    envVar
		image   string
		timeout time.Duration
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := stageInit(tt.args.pidFile, tt.args.vars, tt.args.image, tt.args.timeout); (err != nil) != tt.wantErr {
				t.Errorf("stageInit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
