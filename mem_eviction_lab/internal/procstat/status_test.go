package procstat

import (
	"os"
	"testing"
)

func TestParseStatusAndReadSelf(t *testing.T) {
	sample := []byte(`Name:	otelcol-contrib
Umask:	0022
State:	S (sleeping)
VmHWM:	  153600 kB
VmRSS:	   61440 kB
RssAnon:	   40960 kB
RssFile:	   20480 kB
RssShmem:	       0 kB
VmSwap:	     512 kB
`)
	st, err := ParseStatus(sample)
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if st.VmHWMBytes != 153600*1024 || st.VmRSSBytes != 61440*1024 ||
		st.RssAnonBytes != 40960*1024 || st.RssFileBytes != 20480*1024 || st.VmSwapBytes != 512*1024 {
		t.Fatalf("unexpected parsed status: %+v", st)
	}

	selfSt, err := ReadPID(os.Getpid())
	if err != nil {
		t.Fatalf("ReadPID(self): %v", err)
	}
	if selfSt.VmRSSBytes == 0 {
		t.Fatalf("expected non-zero VmRSSBytes for current process, got %+v", selfSt)
	}
}
