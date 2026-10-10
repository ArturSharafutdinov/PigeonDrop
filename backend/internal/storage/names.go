package storage

import "github.com/google/uuid"

// RecordingName is the name a message's original recording is stored under: the file named after the message's id,
// so that nothing a client chooses ever becomes part of a path. The upload writes it, the player and the speech
// recognition read it; they all take it from here so that they cannot disagree.
func RecordingName(id uuid.UUID) string { return id.String() + ".opus" }
