// Entity: Signed Delivery Fixture (003 spec Key Entities) — raw pull_request
// webhook payloads as byte slices.
//
// They are []byte, not structs, on purpose: the HMAC in sign.go must be
// computed over the exact bytes the gateway reads (fixtures.go → signedRequest →
// gateway → internal/verify), so a re-marshal can never drift the signed bytes
// from the delivered bytes (T005). The shapes mirror the subset the gateway
// decodes in internal/webhook/webhook.go (event struct).
package integration

import "fmt"

// Fixture identities. The GitHub stub's path regexes accept these owner/repo/n,
// and the head SHA lets the golden path assert the Check Run targets it.
const (
	fxOwner   = "octo-org"
	fxRepo    = "hello-world"
	fxNumber  = 42
	fxHeadSHA = "d6fde92930d4715a2b49857d24b940956b26d2d3"
	fxAuthor  = "contributor-jane"
	// slopLabel must match the gateway's SLOP_LABEL (config default).
	slopLabel = "needs-human-review"
)

// openedPayload is a slop PR "opened" delivery — the golden-path input. It
// carries a head SHA so the Check Run has something to attach to.
func openedPayload() []byte { return prPayload("opened") }

// synchronizePayload is the same PR re-pushed ("synchronize") — also actionable.
func synchronizePayload() []byte { return prPayload("synchronize") }

// prPayload builds a pull_request delivery for the given action with the fixture
// identity. Human (non-bot) author so the bot-skip gate does not drop it.
func prPayload(action string) []byte {
	return []byte(fmt.Sprintf(`{
  "action": %q,
  "number": %d,
  "pull_request": {
    "title": "Add feature X",
    "user": { "login": %q },
    "head": { "sha": %q }
  },
  "repository": {
    "owner": { "login": %q },
    "name": %q
  }
}`, action, fxNumber, fxAuthor, fxHeadSHA, fxOwner, fxRepo))
}

// unlabeledPayload is a maintainer removing the slop label — the feedback
// signal (US3). The label name matches the gateway's configured SlopLabel, so
// recordDisagreement fires; any other label would be a normal non-actionable
// event.
func unlabeledPayload() []byte {
	return []byte(fmt.Sprintf(`{
  "action": "unlabeled",
  "number": %d,
  "pull_request": {
    "title": "Add feature X",
    "user": { "login": %q },
    "head": { "sha": %q }
  },
  "repository": {
    "owner": { "login": %q },
    "name": %q
  },
  "label": { "name": %q }
}`, fxNumber, fxAuthor, fxHeadSHA, fxOwner, fxRepo, slopLabel))
}

// prIdentity is the "owner/repo#n" string the feedback Signal records for the
// fixture PR — used by US3 assertions.
func prIdentity() string { return fmt.Sprintf("%s/%s#%d", fxOwner, fxRepo, fxNumber) }
