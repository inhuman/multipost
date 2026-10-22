import Evident

/-!
# A post never lands twice on one platform — even across a crash

The project keeps a journal: per platform, which posts went there, found by
the post's folder or by the hash of the text the platform gets (so the same
text from another folder counts too). An entry is `pending` from the moment
before the platform is called until its answer, then `posted`. A crash, a
lost connection or a half-done post leaves `pending`: the outcome is unknown.

For one platform, a run:

* skips it if the journal says `posted` (unless `-force`);
* on `pending`, asks the platform for its recent posts when it can list them:
  found — recorded as posted, with a warning, not posted again; absent —
  posted; when it cannot list (Telegram bots can't read a channel), skips it
  with a warning and a failing exit: a human looks at the channel and reruns
  with `-force`;
* otherwise writes `pending` to the journal, THEN publishes;
* `ok` → `posted`; `rejected` (the platform answered with an error, nothing
  went out) → the journal as it was; `lost` / `halfSent` → `pending` stays;
* never downgrades `posted`;
* goes on to the next platform whatever happened here.
-/

namespace NoDoublePost
open Evident

inductive Record where
  | none | pending | posted
  deriving DecidableEq, Repr, Lean.ToJson

/-- How the journal entry was found: by this folder, or by the same text
posted from another folder. -/
inductive FoundBy where
  | dir | text
  deriving DecidableEq, Repr, Lean.ToJson

/-- The platform's own history, consulted only for `pending`. -/
inductive Remote where
  | cannotList | found | absent
  deriving DecidableEq, Repr, Lean.ToJson

inductive Result where
  | ok | rejected | lost | halfSent
  deriving DecidableEq, Repr, Lean.ToJson

structure Outcome where
  attempted : Bool
  journaledFirst : Bool
  record : Record
  warned : Bool
  failed : Bool
  nextAttempted : Bool
  deriving DecidableEq, Repr, Lean.ToJson

instance : Finite Record where
  all := [.none, .pending, .posted]
  complete := by intro x; cases x <;> decide

instance : Finite FoundBy where
  all := [.dir, .text]
  complete := by intro x; cases x <;> decide

instance : Finite Remote where
  all := [.cannotList, .found, .absent]
  complete := by intro x; cases x <;> decide

instance : Finite Result where
  all := [.ok, .rejected, .lost, .halfSent]
  complete := by intro x; cases x <;> decide

def Result.sent? : Result → Bool
  | .ok | .lost | .halfSent => true
  | .rejected => false

def skip (r : Record) (warned failed : Bool) : Outcome := ⟨false, false, r, warned, failed, true⟩

def attempt (r : Record) (res : Result) : Outcome :=
  let after := match res with
    | .ok => Record.posted
    | .rejected => r
    | .lost | .halfSent => if r = .posted then .posted else .pending
  ⟨true, true, after, false, res != .ok, true⟩

/-- `by` does not change the decision: a match by text is as binding as a
match by folder. It is an input so the harness must show both work. -/
def run (r : Record) (_by : FoundBy) (remote : Remote) (res : Result) (force : Bool) : Outcome :=
  if force then attempt r res
  else match r, remote with
    | .posted, _ => skip .posted false false
    | .pending, .found => skip .posted true false
    | .pending, .cannotList => skip .pending true true
    | .pending, .absent => attempt r res
    | .none, _ => attempt r res

/-- Without `-force` a platform that has the post — by the journal or by its
own history — is never asked again. -/
theorem no_double_post :
    ∀ b rm res, (run .posted b rm res false).attempted = false ∧
      (run .pending b .found res false).attempted = false := by
  decide

/-- An unknown outcome is never retried blindly: without a way to look at
the platform, it is skipped loudly. -/
theorem unknown_never_retried_blindly :
    ∀ b res, run .pending b .cannotList res false = ⟨false, false, .pending, true, true, true⟩ := by
  decide

/-- The journal says `pending` before the platform is called, so a crash at
any moment leaves a trace. -/
theorem journaled_before_publish :
    ∀ r b rm res f, (run r b rm res f).attempted = true → (run r b rm res f).journaledFirst = true := by
  decide

/-- Whatever may have reached the platform is never forgotten: after `lost`
or `halfSent` the journal still says `pending` (or `posted`). -/
theorem maybe_sent_never_forgotten :
    ∀ r b rm res f, (run r b rm res f).attempted = true → res.sent? = true →
      (run r b rm res f).record ≠ .none := by
  decide

/-- A rejected attempt leaves the journal as it was. -/
theorem rejected_changes_nothing :
    ∀ r b rm f, (run r b rm .rejected f).attempted = true → (run r b rm .rejected f).record = r := by
  decide

/-- `posted` is never downgraded. -/
theorem posted_never_lost :
    ∀ b rm res f, (run .posted b rm res f).record = .posted := by
  decide

/-- A post found on the platform is recorded and reported. -/
theorem found_is_recorded :
    ∀ b res, (run .pending b .found res false).record = .posted ∧ (run .pending b .found res false).warned = true := by
  decide

/-- The run fails exactly when an attempt did not succeed or an unknown
outcome could not be checked. -/
theorem fails_iff :
    ∀ r b rm res f, (run r b rm res f).failed = true ↔
      (((run r b rm res f).attempted = true ∧ res ≠ .ok) ∨
       (f = false ∧ r = .pending ∧ rm = .cannotList)) := by
  decide

/-- Nothing here stops the next platform. -/
theorem next_always_runs : ∀ r b rm res f, (run r b rm res f).nextAttempted = true := by
  decide

end NoDoublePost
