import Evident

/-!
# A post never lands twice on one platform

Each post folder keeps a record of where it already went. A run posts to
the selected platforms in order (here two, `a` then `b`):

* a platform the record has is skipped, unless `-force`;
* a failure is logged and the run goes on to the next platform;
* only a success is recorded; an old record survives a failed `-force`;
* the run exits with an error if any attempt failed, so a rerun is due —
  and the rerun skips what already went.
-/

namespace NoDoublePost
open Evident

inductive Target where
  | a | b
  deriving DecidableEq, Repr, Lean.ToJson

/-- What the platform does if asked to publish. -/
inductive Result where
  | ok | fail
  deriving DecidableEq, Repr, Lean.ToJson

instance : Finite Result where
  all := [.ok, .fail]
  complete := by intro x; cases x <;> decide

structure Outcome where
  attempted : List Target
  recorded : List Target
  failed : Bool
  deriving DecidableEq, Repr, Lean.ToJson

/-- One platform: does the run attempt it, is it recorded after, did it fail. -/
def step (posted : Bool) (r : Result) (force : Bool) : Bool × Bool × Bool :=
  if posted && !force then (false, true, false)
  else match r with
    | .ok => (true, true, false)
    | .fail => (true, posted, true)

def run (aPosted : Bool) (aResult : Result) (bPosted : Bool) (bResult : Result) (force : Bool) : Outcome :=
  let (aTry, aRec, aFail) := step aPosted aResult force
  let (bTry, bRec, bFail) := step bPosted bResult force
  let pick (x : Bool) (t : Target) := if x then [t] else []
  ⟨pick aTry .a ++ pick bTry .b, pick aRec .a ++ pick bRec .b, aFail || bFail⟩

/-- Without `-force` a platform that already has the post is never asked again. -/
theorem no_double_post :
    ∀ ar bp br, Target.a ∉ (run true ar bp br false).attempted := by
  decide

/-- A failed attempt is never recorded as a post. -/
theorem failure_not_recorded :
    ∀ bp br f, Target.a ∉ (run false .fail bp br f).recorded := by
  decide

/-- The first platform failing does not stop the second. -/
theorem failure_does_not_stop_others :
    ∀ ap br f, ap = false ∨ f = true → Target.b ∈ (run ap .fail false br f).attempted := by
  decide

/-- Every success is recorded, so the rerun after a failure skips it. -/
theorem success_recorded :
    ∀ ap bp br f, (ap = false ∨ f = true) → Target.a ∈ (run ap .ok bp br f).recorded := by
  decide

/-- The run fails exactly when some attempt failed. -/
theorem fails_iff_attempt_failed :
    ∀ ap ar bp br f, (run ap ar bp br f).failed = true ↔
      ((step ap ar f).1 = true ∧ ar = .fail) ∨ ((step bp br f).1 = true ∧ br = .fail) := by
  decide

/-- A record is never lost, even by a failed `-force`. -/
theorem record_never_lost :
    ∀ ar bp br f, Target.a ∈ (run true ar bp br f).recorded := by
  decide

end NoDoublePost
