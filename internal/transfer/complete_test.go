package transfer

import (
	"errors"
	"testing"
)

func TestCheckCompleteFlagsAShortTransfer(t *testing.T) {
	err := CheckComplete(Request{Size: 1000}, 400)
	if !errors.Is(err, ErrShort) {
		t.Fatalf("CheckComplete(1000, 400) = %v, want ErrShort", err)
	}
}

func TestCheckCompletePassesAFullTransfer(t *testing.T) {
	if err := CheckComplete(Request{Size: 1000}, 1000); err != nil {
		t.Fatalf("CheckComplete(1000, 1000) = %v, want nil", err)
	}
}

// A resumed transfer's byte count starts at Offset and ends at the source's
// full size, so the comparison is against Size either way.
func TestCheckCompletePassesAResumedTransfer(t *testing.T) {
	if err := CheckComplete(Request{Size: 1000, Offset: 600}, 1000); err != nil {
		t.Fatalf("resumed CheckComplete = %v, want nil", err)
	}
}

// An FTP listing that could not parse a size reports 0 for a file that is not
// empty. Failing every such transfer would be worse than not checking.
func TestCheckCompleteSkipsAnUnknownSize(t *testing.T) {
	if err := CheckComplete(Request{Size: 0}, 4096); err != nil {
		t.Fatalf("CheckComplete with no size = %v, want nil", err)
	}
}

// A source that grew after it was listed is not a failure.
func TestCheckCompleteAllowsMoreThanExpected(t *testing.T) {
	if err := CheckComplete(Request{Size: 1000}, 1200); err != nil {
		t.Fatalf("CheckComplete(1000, 1200) = %v, want nil", err)
	}
}
