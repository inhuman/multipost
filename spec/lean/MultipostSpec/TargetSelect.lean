import Evident

/-!
# Which platforms a run posts to

The post (its `targets:`) or `-only` may name platforms; otherwise every
configured one goes. Three platforms stand for the three kinds of setup:

* `tg` — an API platform: configured or not;
* `max` — an API platform with a numeric id: configured, not configured, or
  configured with a typo in the id;
* `vk` — a browser platform: configured, not configured, or its URL set while
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

inductive Vk where
  | ready | unset | noFlow
  deriving DecidableEq, Repr, Lean.ToJson

/-- What was asked: nothing named, or a list. -/
inductive Request where
  | byDefault | onlyTg | onlyVk | onlyMax | all
  deriving DecidableEq, Repr, Lean.ToJson

inductive Target where
  | tg | max | vk
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

instance : Finite Vk where
  all := [.ready, .unset, .noFlow]
  complete := by intro x; cases x <;> decide

instance : Finite Target where
  all := [.tg, .max, .vk]
  complete := by intro x; cases x <;> decide

instance : Finite Request where
  all := [.byDefault, .onlyTg, .onlyVk, .onlyMax, .all]
  complete := by intro x; cases x <;> decide

/-- How a platform's setup reads: ready, missing (skippable), broken. -/
inductive Setup where
  | ready | missing | broken
  deriving DecidableEq, Repr

def setup (tg : Tg) (mx : MaxCfg) (vk : Vk) : Target → Setup
  | .tg => if tg = .ready then .ready else .missing
  | .max => match mx with
    | .ready => .ready
    | .unset => .missing
    | .badId => .broken
  | .vk => if vk = .ready then .ready else .missing

def named : Request → List Target
  | .byDefault => [.tg, .max, .vk]
  | .onlyTg => [.tg]
  | .onlyVk => [.vk]
  | .onlyMax => [.max]
  | .all => [.tg, .max, .vk]

def fail : Outcome := ⟨[], true⟩

def choose (tg : Tg) (mx : MaxCfg) (vk : Vk) (req : Request) : Outcome :=
  let s := setup tg mx vk
  let names := named req
  if names.any (fun t => s t == .broken) then fail
  else if req != .byDefault && names.any (fun t => s t != .ready) then fail
  else
    let sel := names.filter (fun t => s t == .ready)
    if sel.isEmpty then fail else ⟨sel, false⟩

/-- Nothing unready is ever posted to. -/
theorem never_selects_unready :
    ∀ tg mx vk req t, t ∈ (choose tg mx vk req).selected → setup tg mx vk t = .ready := by
  decide

/-- A failed selection posts nowhere. -/
theorem failure_posts_nowhere :
    ∀ tg mx vk req, (choose tg mx vk req).failed = true → (choose tg mx vk req).selected = [] := by
  decide

/-- A named request is all or nothing. -/
theorem named_is_all_or_nothing :
    ∀ tg mx vk req, req ≠ .byDefault → (choose tg mx vk req).failed = false →
      (choose tg mx vk req).selected = named req := by
  decide

/-- By default every ready platform goes, unless a broken one stops the run. -/
theorem default_takes_every_ready :
    ∀ tg mx vk t, mx ≠ .badId → setup tg mx vk t = .ready →
      t ∈ (choose tg mx vk .byDefault).selected := by
  decide

/-- A typo in a set value is loud wherever that platform is asked for. -/
theorem typo_is_loud :
    ∀ tg vk req, Target.max ∈ named req → (choose tg .badId vk req).failed = true := by
  decide

/-- An unready platform that nobody asked for does not stop the others. -/
theorem unasked_does_not_block :
    ∀ mx vk, (choose .ready mx vk .onlyTg) = ⟨[.tg], false⟩ := by
  decide

end TargetSelect
