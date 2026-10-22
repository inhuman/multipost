// Package spec holds what multipost must do, as Lean models proven in
// lean/MultipostSpec. `lake env lean Export.lean` in lean/ writes the
// exports (*.json here), evident.lock pins them; regenerate and re-lock
// only together with the model. The harness in the main package drives the
// live code through every row of every model.
package spec

import (
	_ "embed"

	"github.com/inhuman/evident"
	"github.com/inhuman/evident/evidentlean"
)

//go:embed evident.lock
var lock []byte

//go:embed tg-layout.json
var tgLayoutModel []byte

//go:embed route-guard.json
var routeGuardModel []byte

//go:embed targets-select.json
var targetsSelectModel []byte

//go:embed no-double-post.json
var noDoublePostModel []byte

//go:embed post-text.json
var postTextModel []byte

// TGLayout: which Bot API calls a post becomes, for every media set (none,
// one image, one video, an album, 11+ files, a file over 50 MB) and text
// length (≤1024, ≤4096, longer). Lean: text_sent_once, refused_sends_nothing,
// caption_only_short, media_once_and_first, refused_iff_over_limit.
//
// Harness facts: media, text, outcome {calls: methods the fake Bot API got,
// in order, as "message"|"photo"|"video"|"album"; caption: whether the text
// rode on the media call; refused: Publish returned an error}.
var TGLayout = evidentlean.MustRequirement(tgLayoutModel, lock,
	"a post becomes one media call and the text exactly once, or nothing at all")

// RouteGuard: whether a host may be used, for each route kind and up to two
// resolved addresses routed through the VPN or the physical interface.
// Lean: direct_never_through_vpn, vpn_never_direct, one_wrong_address_refuses,
// unresolved_refused, any_always_allowed.
//
// Harness facts: want, a, b (iface or nil), allowed (the guard returned nil).
var RouteGuard = evidentlean.MustRequirement(routeGuardModel, lock,
	"traffic leaves only the way its platform needs, judged by every address")

// TargetsSelect: which platforms a run posts to, for each setup state of an
// API platform, an API platform with a numeric id, a browser platform, and
// each request. Lean: never_selects_unready, named_is_all_or_nothing,
// typo_is_loud, failure_posts_nowhere, default_takes_every_ready.
//
// Harness facts: tg, max, vk, request, outcome {selected, failed}.
var TargetsSelect = evidentlean.MustRequirement(targetsSelectModel, lock,
	"named platforms go all or nothing, by default every ready one, a typo always fails")

// NoDoublePost: what a run attempts and records for two platforms, each
// already posted or not and succeeding or failing, with and without -force.
// Lean: no_double_post, failure_not_recorded, failure_does_not_stop_others,
// record_never_lost, fails_iff_attempt_failed.
//
// Harness facts: a_posted, a_result, b_posted, b_result, force,
// outcome {attempted: Publish calls, recorded: the state file after the run,
// failed: publishAll reported a failure}.
var NoDoublePost = evidentlean.MustRequirement(noDoublePostModel, lock,
	"a post never lands twice on a platform and a failure never stops the rest")

// PostText: which text a platform gets for each mode, paid or free platform,
// and paid link given or not. Lean: paid_gets_body, teaser_never_leaks_body,
// link_never_lost, full_is_same_everywhere.
//
// Harness facts: mode, paid, link, text (which of body / teaser /
// teaser + link TextFor returned).
var PostText = evidentlean.MustRequirement(postTextModel, lock,
	"paid platforms get the whole post, free ones never get the paid text")

// All lists every requirement; `evident gate` fails if one was not judged ok.
var All = []evident.Requirement{TGLayout, RouteGuard, TargetsSelect, NoDoublePost, PostText}
