import Evident

/-!
# How a post is laid out into Telegram Bot API calls

Bot API limits: a caption holds 1024 characters, a message 4096, a bot
uploads at most 50 MB per file, an album holds at most 10 items.

* No media: the text is one message.
* Media and a short text: one media call (photo, video or album) with the
  text as its caption.
* Media and a long text: the media call without a caption, then the text as
  a message — the channel reads it right under the media.
* A text over 4096, more than 10 media or a file over 50 MB: refused before
  any call, so the channel never gets half a post.
-/

namespace TgLayout
open Evident

/-- What the post carries. `album` is 2..10 files; `tooMany` is 11+;
`tooBig` is any file over 50 MB. -/
inductive Media where
  | empty | image | video | album | tooMany | tooBig
  deriving DecidableEq, Repr, Lean.ToJson

/-- Text length: `short` ≤ 1024, `long` 1025..4096, `over` > 4096. -/
inductive Text where
  | short | long | over
  deriving DecidableEq, Repr, Lean.ToJson

inductive Call where
  | message | photo | video | album
  deriving DecidableEq, Repr, Lean.ToJson

/-- Calls in order; whether the text rode as a caption; whether the post
was refused. -/
structure Outcome where
  calls : List Call
  caption : Bool
  refused : Bool
  deriving DecidableEq, Repr, Lean.ToJson

instance : Finite Media where
  all := [.empty, .image, .video, .album, .tooMany, .tooBig]
  complete := by intro x; cases x <;> decide

instance : Finite Text where
  all := [.short, .long, .over]
  complete := by intro x; cases x <;> decide

def mediaCall : Media → Call
  | .image => .photo
  | .video => .video
  | _ => .album

def refusal : Outcome := ⟨[], false, true⟩

def layout : Media → Text → Outcome
  | _, .over => refusal
  | .tooMany, _ => refusal
  | .tooBig, _ => refusal
  | .empty, _ => ⟨[.message], false, false⟩
  | m, .short => ⟨[mediaCall m], true, false⟩
  | m, .long => ⟨[mediaCall m, .message], false, false⟩

/-- Refused exactly when a Bot API limit would be broken. -/
theorem refused_iff_over_limit :
    ∀ m t, (layout m t).refused = true ↔ (t = .over ∨ m = .tooMany ∨ m = .tooBig) := by
  decide

/-- A refused post makes no call at all: no half-post in the channel. -/
theorem refused_sends_nothing :
    ∀ m t, (layout m t).refused = true → (layout m t).calls = [] := by
  decide

/-- The text reaches the channel exactly once: as a caption or as a message. -/
theorem text_sent_once :
    ∀ m t, (layout m t).refused = false →
      (layout m t).calls.count .message + (if (layout m t).caption then 1 else 0) = 1 := by
  decide

/-- Only a short text rides as a caption; a long one never gets cut. -/
theorem caption_only_short :
    ∀ m t, (layout m t).caption = true → t = .short := by
  decide

/-- All media go in one call, and it comes first. -/
theorem media_once_and_first :
    ∀ m t, m ≠ .empty → (layout m t).refused = false →
      (layout m t).calls.head? = some (mediaCall m) ∧
      ((layout m t).calls.filter (· != .message)).length = 1 := by
  decide

end TgLayout
