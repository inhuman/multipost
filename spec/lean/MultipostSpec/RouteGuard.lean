import Evident

/-!
# Which way a platform's traffic may leave the machine

Telegram works only through the VPN; MAX only past it (through the
VPN they hang or serve anti-bot stubs); Boosty and Sponsr work either way.
Before a host is used, every IPv4 address it resolves to is checked: the
HTTP client may pick any of them, so one address on the wrong side is
enough to refuse. A host that does not resolve is refused too.

`a` and `b` are up to two resolved addresses and the interface each one
routes through; `none` means the address is absent.
-/

namespace RouteGuard
open Evident

inductive Want where
  | vpn | direct | any
  deriving DecidableEq, Repr, Lean.ToJson

inductive Iface where
  | vpn | phys
  deriving DecidableEq, Repr, Lean.ToJson

instance : Finite Want where
  all := [.vpn, .direct, .any]
  complete := by intro x; cases x <;> decide

instance : Finite Iface where
  all := [.vpn, .phys]
  complete := by intro x; cases x <;> decide

def fits : Want → Iface → Bool
  | .vpn, .vpn => true
  | .direct, .phys => true
  | .any, _ => true
  | _, _ => false

def allowed (w : Want) (a b : Option Iface) : Bool :=
  match w with
  | .any => true
  | _ =>
    let ips := a.toList ++ b.toList
    !ips.isEmpty && ips.all (fits w)

/-- Boosty and Sponsr are never blocked by the guard. -/
theorem any_always_allowed : ∀ a b, allowed .any a b = true := by
  decide

/-- Nothing meant to go direct ever leaves through the VPN. -/
theorem direct_never_through_vpn :
    ∀ a b, allowed .direct a b = true → a ≠ some .vpn ∧ b ≠ some .vpn := by
  decide

/-- Nothing meant for the VPN ever leaves directly. -/
theorem vpn_never_direct :
    ∀ a b, allowed .vpn a b = true → a ≠ some .phys ∧ b ≠ some .phys := by
  decide

/-- One address on the wrong side refuses the host, whatever the other is. -/
theorem one_wrong_address_refuses :
    ∀ a, allowed .direct a (some .vpn) = false ∧ allowed .vpn a (some .phys) = false := by
  decide

/-- An unresolved host is refused unless the platform does not care. -/
theorem unresolved_refused : ∀ w, w ≠ .any → allowed w none none = false := by
  decide

/-- Some address set passes for every route kind: the guard is not a wall. -/
theorem every_kind_passable : ∀ w, ∃ a, allowed w a none = true := by
  decide

end RouteGuard
