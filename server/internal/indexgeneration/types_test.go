package indexgeneration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPublicGenerationJSONDoesNotExposeActorUUID(t *testing.T) {
	actorID := uuid.New()
	build, err := json.Marshal(Build{
		CreatedByUserID: &actorID,
		CreatorPresent:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := json.Marshal(Event{ActorPresent: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string][]byte{"build": build, "event": event} {
		if strings.Contains(string(encoded), actorID.String()) {
			t.Fatalf("%s JSON exposed actor UUID: %s", name, encoded)
		}
		if !strings.Contains(string(encoded), `"actor_present":true`) &&
			!strings.Contains(string(encoded), `"creator_present":true`) {
			t.Fatalf("%s JSON omitted actor presence: %s", name, encoded)
		}
	}
}
