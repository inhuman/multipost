import Evident

/-!
# Which text a platform gets

A post is either free (`full`: everybody reads all of it) or paid
(`teaser`: free platforms get a teaser, the full text stays behind the
paywall). Paid platforms (Boosty, Sponsr) always get the full text. The
teaser carries the link to the paid post when one is given.
-/

namespace PostText
open Evident

inductive Mode where
  | full | teaser
  deriving DecidableEq, Repr, Lean.ToJson

inductive Text where
  | body | teaser | teaserLink
  deriving DecidableEq, Repr, Lean.ToJson

instance : Finite Mode where
  all := [.full, .teaser]
  complete := by intro x; cases x <;> decide

def textFor : Mode → Bool → Bool → Text
  | .full, _, _ => .body
  | .teaser, true, _ => .body
  | .teaser, false, true => .teaserLink
  | .teaser, false, false => .teaser

/-- A paid platform always gets the whole post. -/
theorem paid_gets_body : ∀ m l, textFor m true l = .body := by
  decide

/-- A free post reads the same everywhere. -/
theorem full_is_same_everywhere : ∀ p l, textFor .full p l = .body := by
  decide

/-- The paid text never leaks to a free platform. -/
theorem teaser_never_leaks_body : ∀ l, textFor .teaser false l ≠ .body := by
  decide

/-- A given paid link is never dropped from the teaser. -/
theorem link_never_lost : textFor .teaser false true = .teaserLink := by
  decide

end PostText
