// Package TSR provides the API to make the program run in the background,
// what used to be called "Terminate and Stay Resident" back in the days.
package gotsr

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_readPID(t *testing.T) {
	tests := []struct {
		name     string
		contents []byte
		datalen  int
		want     int
		wantData []string
		wantErr  bool
	}{
		{
			"number",
			[]byte("12345"),
			0,
			12345,
			[]string{},
			false,
		},
		{
			"number with a new line",
			[]byte("12345\n"),
			0,
			12345,
			[]string{},
			false,
		},
		{
			"empty",
			[]byte(""),
			0,
			0,
			[]string{},
			true,
		},
		{
			"not a number",
			[]byte("test"),
			0,
			0,
			[]string{},
			true,
		},
		{
			"additional data",
			[]byte("12345\ntest"),
			1,
			12345,
			[]string{"test"},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "1.txt")
			if err := os.WriteFile(filename, tt.contents, 0666); err != nil {
				t.Fatal(err)
			}
			// initialize the data slice
			data := make([]*string, tt.datalen)
			for i := range data {
				data[i] = new(string)
			}

			got, err := readPID(filename, data...)
			if (err != nil) != tt.wantErr {
				t.Errorf("readPID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("readPID() = %v, want %v", got, tt.want)
			}
			for i, d := range data {
				if *d != tt.wantData[i] {
					t.Errorf("readPID() data[%d] = %v, want %v", i, *d, tt.wantData[i])
				}
			}
		})
	}
}

func Test_hash(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			"filename",
			args{"test.pid"},
			"EF61F1A18EA6B65A6B571B3AFE264595AF9032DAE597550C22EEFE09",
		},
		{
			"another filename",
			args{"test.pi"},
			"289FFB825DEB0AC11B934A86E70BFD02FBBB094C74C6F13522D1997A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hash(tt.args.s); got != tt.want {
				t.Errorf("hash() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_pidFromExe(t *testing.T) {
	type args struct {
		executable string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			"nix no path",
			args{"./test"},
			"test.pid",
		},
		{
			"win no path",
			args{"test.exe"},
			"test.pid",
		},
		{
			"nix, with path",
			args{"/usr/local/bin/proggy"},
			"proggy.pid",
		},
		//{
		//	"win, with path",
		//	args{"C:\\PROGRAM FILES\\SOME PROGRAM\\run.exe"},
		//	"run.pid",
		//},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pidFromExe(tt.args.executable); got != tt.want {
				t.Errorf("pidFromExe() = %v, want %v", got, tt.want)
			}
		})
	}
}
