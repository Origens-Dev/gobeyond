# Operator mailbox budget

`AIConfig.VoiceBudgetPolicy: "operator_mailbox_v1"` is a static authored opt-in
frozen into the voice manifest and its digest. Activation requires current
agent scope for `call-operator`, eligible read declarations for
`list-text-messages` and `get-text-message`, and `dial-contact` call control.
Unknown or mismatched declarations fail closed. Old manifests omit the additive
field and retain their existing canonical bytes.

The platform must mint the matching policy in signed GrantClaims, verify the
current owner and frozen manifest, and derive VoiceSessionInput.BudgetPolicy
from those verified claims. Never copy a policy from client requests or session
metadata. The host must additionally require both mailbox reads in its enabled
selection before selecting this budget. Public runtime helpers validate the
scope and manifest; they do not verify signatures themselves.

The budget has independent fixed buckets: four lists, twelve selected message
reads, two placement operations shared by directory search and dial, and one
hangup. Playback marking has twelve reserved slots in the design, but no mark
mutation is enabled by this version; it requires a separate authenticated
playback/mutation contract. Message reads cannot consume placement or hangup
capacity. Exact replays retain cached results without another charge; altered
and cross-tool replay identities fail closed.

The durable workflow records `operator-mailbox-budget-v1` with GetVersion. Old
histories retain the existing shared two-operation behavior. New workflows use
the policy only from their trusted initial input; request-only selectors cannot
expand it. Activities repeat the frozen registry declaration check.

The verified in-process CallControlConfig.BudgetPolicy allows at most 32
assistant turns for this policy. Blank policy retains the ten-turn maximum.
This package does not activate mailbox tools, add generic mutation dispatch, or
claim playback completion. The host must provide policy selection from signed
claims; model turn completion and a permissive playout timeout cannot authorize
marking a message read.
