package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSubmissionOwnershipIsNotJSON(t *testing.T) {
	sub := Submission{ID: "submission", OwnerService: "private-service", OwnerUser: "private-user"}
	contents, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "private-") || strings.Contains(string(contents), "owner") {
		t.Fatalf("submission JSON exposed ownership: %s", contents)
	}
	var decoded Submission
	if err := json.Unmarshal([]byte(`{"id":"submission","OwnerService":"forged-service","OwnerUser":"forged-user","ownerService":"forged-service","ownerUser":"forged-user"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OwnerService != "" || decoded.OwnerUser != "" {
		t.Fatal("submission JSON accepted forged ownership")
	}
}

func TestScopedPersistenceRejectsEmptyIdentities(t *testing.T) {
	store := NewPostgresSubmissionStore(nil)
	for _, identity := range [][2]string{{"", ""}, {"service", ""}, {"", "user"}} {
		if _, found, err := store.GetOwned(context.Background(), "id", identity[0], identity[1]); found || err != nil {
			t.Fatalf("GetOwned() = %v, %v; want not found", found, err)
		}
		if deleted, err := store.DeleteOwned(context.Background(), "id", identity[0], identity[1]); deleted || err != nil {
			t.Fatalf("DeleteOwned() = %v, %v; want not found", deleted, err)
		}
	}
}
