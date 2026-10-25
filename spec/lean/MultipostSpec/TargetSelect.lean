import Evident

/-!
# Which platforms a run posts to

The post (its `targets:`) or `-only` may name platforms; otherwise every
configured one goes. Three platforms stand for the three kinds of setup:

* `tg` — an API platform: configured or not;
* `max` — an API platform with a numeric id: configured, not configured, or
  configured with a typo in the id;
* `boosty` — a browser platform: configured, not configured, or its URL set while
  its browser flow is not recorded yet (counts as not configured).

Named platforms must all be ready, or nothing goes: a request is all or
nothing. By default the unready ones are skipped and reported. A value that
is set but broken always fails the run: skipping it would hide the typo.
-/

namespace TargetSelect
open Evident

inductive Tg where
  | ready | unset
  deriving DecidableEq, Repr, Lean.ToJson

inductive MaxCfg where
  | ready | unset | badId
  deriving DecidableEq, Repr, Lean.ToJson

inductive Boosty where
  | ready | unset | noFlow
  deriving DecidableEq, Repr, Lean.ToJson

/-- What was asked: nothing named, or a list. -/
inductive Request where
  | byDefault | onlyTg | onlyBoosty | onlyMax | all
  deriving DecidableEq, Repr, Lean.ToJson

inductive Target where
  | tg | max | boosty
  deriving DecidableEq, Repr, Lean.ToJson

structure Outcome where
  selected : List Target
  failed : Bool
  deriving DecidableEq, Repr, Lean.ToJson

instance : Finite Tg where
  all := [.ready, .unset]
  complete := by intro x; cases x <;> decide

instance : Finite MaxCfg where
  all := [.ready, .unset, .badId]
  complete := by intro x; cases x <;> decide

instance : Finite Boosty where
  all := [.ready, .unset, .noFlow]
  complete := by intro x; cases x <;> decide

instance : Finite Target where
  all := [.tg, .max, .boosty]
  complete := by intro x; cases x <;> decide

instance : Finite Request where
  all := [.byDefault, .onlyTg, .onlyBoosty, .onlyMax, .all]
  complete := by intro x; cases x <;> decide

/-- How a platform's setup reads: ready, missing (skippable), broken. -/
inductive Setup where
  | ready | missing | broken
  deriving DecidableEq, Repr

def setup (tg : Tg) (mx : MaxCfg) (boosty : Boosty) : Target → Setup
  | .tg => if tg = .ready then .ready else .missing
  | .max => match mx with
    | .ready => .ready
    | .unset => .missing
    | .badId => .broken
  | .boosty => if boosty = .ready then .ready else .missing

def named : Request → List Target
  | .byDefault => [.tg, .max, .boosty]
  | .onlyTg => [.tg]
  | .onlyBoosty => [.boosty]
  | .onlyMax => [.max]
  | .all => [.tg, .max, .boosty]

def fail : Outcome := ⟨[], true⟩

def choose (tg : Tg) (mx : MaxCfg) (boosty : Boosty) (req : Request) : Outcome :=
  let s := setup tg mx boosty
  let names := named req
  if names.any (fun t => s t == .broken) then fail
  else if req != .byDefault && names.any (fun t => s t != .ready) then fail
  else
    let sel := names.filter (fun t => s t == .ready)
    if sel.isEmpty then fail else ⟨sel, false⟩

/-- Nothing unready is ever posted to. -/
theorem never_selects_unready :
    ∀ tg mx boosty req t, t ∈ (choose tg mx boosty req).selected → setup tg mx boosty t = .ready := by
  decide

/-- A failed selection posts nowhere. -/
theorem failure_posts_nowhere :
    ∀ tg mx boosty req, (choose tg mx boosty req).failed = true → (choose tg mx boosty req).selected = [] := by
  decide

/-- A named request is all or nothing. -/
theorem named_is_all_or_nothing :
    ∀ tg mx boosty req, req ≠ .byDefault → (choose tg mx boosty req).failed = false →
      (choose tg mx boosty req).selected = named req := by
  decide

/-- By default every ready platform goes, unless a broken one stops the run. -/
theorem default_takes_every_ready :
    ∀ tg mx boosty t, mx ≠ .badId → setup tg mx boosty t = .ready →
      t ∈ (choose tg mx boosty .byDefault).selected := by
  decide

/-- A typo in a set value is loud wherever that platform is asked for. -/
theorem typo_is_loud :
    ∀ tg boosty req, Target.max ∈ named req → (choose tg .badId boosty req).failed = true := by
  decide

/-- An unready platform that nobody asked for does not stop the others. -/
theorem unasked_does_not_block :
    ∀ mx boosty, (choose .ready mx boosty .onlyTg) = ⟨[.tg], false⟩ := by
  decide

end TargetSelect
