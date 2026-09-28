package pythonexec

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIsolatedCPython(t *testing.T) {
	path := os.Getenv("Q4D_PYTHON_RUNTIME_DIR")
	if path == "" {
		t.Skip("set Q4D_PYTHON_RUNTIME_DIR after scripts/fetch-python-runtime.sh to run the real CPython sandbox suite")
	}
	r, err := New(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	t.Setenv("Q4D_PRIVATE_TEST_SECRET", "must-not-be-visible")
	t.Run("standard_library_and_owned_input", func(t *testing.T) {
		result, err := r.Execute(context.Background(), `import json,statistics,datetime,decimal
data=json.load(open('/data/input.json'))
print(json.dumps({'mean':statistics.mean(data['prices']),'decimal':str(decimal.Decimal('104')/decimal.Decimal('100')-1),'date':datetime.date(2026,9,26).isoformat()}))`, []byte(`{"prices":[1,2,3]}`))
		if err != nil || result.Status != "succeeded" {
			t.Fatalf("%+v %v", result, err)
		}
		var value map[string]any
		if json.Unmarshal([]byte(result.Stdout), &value) != nil || value["mean"] != float64(2) || value["decimal"] != "0.04" {
			t.Fatal(result)
		}
	})
	t.Run("no_host_capabilities", func(t *testing.T) {
		source := `import json,os
checks={'env':os.getenv('Q4D_PRIVATE_TEST_SECRET') is None}
for name,code in {
 'host_file':"open('/etc/passwd').read()",
 'traversal':"open('/data/../../etc/passwd').read()",
 'write_input':"f=open('/data/input.json','w'); f.write('changed'); f.flush()",
 'write_file':"open('/tmp/escape','w').write('x')",
 'socket':"__import__('socket').socket()",
 'process':"__import__('subprocess').run(['id'])",
 'ffi':"__import__('ctypes').CDLL(None)",
}.items():
 try:
  exec(code)
  checks[name]=False
 except Exception:
  checks[name]=True
print(json.dumps(checks))`
		result, err := r.Execute(context.Background(), source, []byte(`{}`))
		if err != nil || result.Status != "succeeded" {
			t.Fatalf("%+v %v", result, err)
		}
		var checks map[string]bool
		if json.Unmarshal([]byte(result.Stdout), &checks) != nil || len(checks) != 8 {
			t.Fatal(result)
		}
		for name, blocked := range checks {
			if !blocked {
				t.Errorf("host capability exposed: %s", name)
			}
		}
	})
	t.Run("infinite_loop_is_cancelled", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
		defer cancel()
		start := time.Now()
		result, err := r.Execute(ctx, "while True: pass", nil)
		if err != nil || result.Status != "timed_out" || time.Since(start) > 2*time.Second {
			t.Fatalf("%+v %v", result, err)
		}
	})
	t.Run("output_limit", func(t *testing.T) {
		result, err := r.Execute(context.Background(), "print('x'*100000)", nil)
		if err != nil || result.Status != "output_limit" || len(result.Stdout) > MaxOutputBytes {
			t.Fatalf("%+v %v", result, err)
		}
	})
	t.Run("memory_limit_and_recovery", func(t *testing.T) {
		result, err := r.Execute(context.Background(), "x=bytearray(256*1024*1024)", nil)
		if err != nil || result.Status == "succeeded" {
			t.Fatalf("%+v %v", result, err)
		}
		good, err := r.Execute(context.Background(), "print(42)", nil)
		if err != nil || good.Status != "succeeded" || strings.TrimSpace(good.Stdout) != "42" {
			t.Fatalf("VM did not recover: %+v %v", good, err)
		}
	})
	t.Run("fresh_instance", func(t *testing.T) {
		first, _ := r.Execute(context.Background(), "import builtins; builtins.leaked=99; print('{}')", nil)
		second, _ := r.Execute(context.Background(), "import builtins; print(hasattr(builtins,'leaked'))", nil)
		if first.Status != "succeeded" || second.Status != "succeeded" || strings.TrimSpace(second.Stdout) != "False" {
			t.Fatal(first, second)
		}
	})
}
