import MultipostSpec

#evident_export "tg.layout" TgLayout.layout ["media", "text"] "outcome"
  [TgLayout.refused_iff_over_limit, TgLayout.refused_sends_nothing, TgLayout.text_sent_once,
   TgLayout.caption_only_short, TgLayout.media_once_and_first]
  "../tg-layout.json"

#evident_export "route.guard" RouteGuard.allowed ["want", "a", "b"] "allowed"
  [RouteGuard.any_always_allowed, RouteGuard.direct_never_through_vpn, RouteGuard.vpn_never_direct,
   RouteGuard.one_wrong_address_refuses, RouteGuard.unresolved_refused, RouteGuard.every_kind_passable]
  "../route-guard.json"

#evident_export "targets.select" TargetSelect.choose ["tg", "max", "vk", "request"] "outcome"
  [TargetSelect.never_selects_unready, TargetSelect.failure_posts_nowhere,
   TargetSelect.named_is_all_or_nothing, TargetSelect.default_takes_every_ready,
   TargetSelect.typo_is_loud, TargetSelect.unasked_does_not_block]
  "../targets-select.json"

#evident_export "state.no-double-post" NoDoublePost.run
  ["record", "found_by", "remote", "result", "force"] "outcome"
  [NoDoublePost.no_double_post, NoDoublePost.unknown_never_retried_blindly,
   NoDoublePost.journaled_before_publish, NoDoublePost.maybe_sent_never_forgotten,
   NoDoublePost.rejected_changes_nothing, NoDoublePost.posted_never_lost,
   NoDoublePost.found_is_recorded, NoDoublePost.fails_iff, NoDoublePost.next_always_runs]
  "../no-double-post.json"

#evident_export "post.text" PostText.textFor ["mode", "paid", "link"] "text"
  [PostText.paid_gets_body, PostText.full_is_same_everywhere, PostText.teaser_never_leaks_body,
   PostText.link_never_lost]
  "../post-text.json"
