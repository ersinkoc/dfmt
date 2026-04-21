# DFMT — Brand Guidelines

| Field | Value |
| --- | --- |
| Document | `BRANDING.md` |
| Status | v1.0 |
| Product | DFMT (Don't Fuck My Tokens) |
| Portfolio | ECOSTACK TECHNOLOGY OÜ — `ersinkoc` |
| Sibling | DFMC (Don't Fuck My Code) |
| Date | 2026-04-20 |

---

## 1. Name

**DFMT** — pronounced "dee-eff-em-tee" in conversation, or "don't fuck my tokens" when the full form is invoked.

The full form is part of the brand. It is aggressive on purpose: developers running AI coding agents are tired of watching context windows fill with useless tool output, of losing an hour's worth of work to a compaction, of re-explaining themselves to a model that just forgot. The name acknowledges that frustration directly. Softening it — "ProtectMyTokens," "ContextGuard," "TokenSaver" — would signal a different product: corporate, safe, forgettable. DFMT is none of those.

**Portfolio context:** DFMT is the sibling of **DFMC (Don't Fuck My Code)** — the same portfolio, the same philosophy ("#NOFORKANYMORE," single binary, stdlib-first), the same voice. Where DFMC protects code quality during AI pair-programming, DFMT protects context and tokens. Together they frame a stance: *"We take what's yours seriously. We don't let it get fucked up."*

Expected third sibling (not yet built): **DFMS** (Don't Fuck My Setup) — ergonomics and config protection. Reserved. The `DFM_` namespace is intentional branding consolidation.

---

## 2. One-Line Positioning

> **DFMT is a local daemon that keeps AI coding agents from wasting your context window — and from losing your working state when the conversation resets.**

Three-part structure:

1. **What it is.** A local daemon.
2. **What problem it solves** (first half). Tool output flooding the context window.
3. **What problem it solves** (second half). Working state lost on compaction / fresh session.

The one-liner names both pain points because each alone undersells the product. Tool-output compression without session memory is half the battle. Session memory without output compression is the other half. DFMT is the whole.

---

## 3. Taglines

**Primary:**

> **Your tokens, your work, your agent — undisturbed.**

Direct, personal, three-beat. Claims DFMT's domain: tokens (budget), work (session state), agent (the thing you've configured). "Undisturbed" is the promise.

**Alternates (for different contexts):**

- **On technical audiences:** *"Context discipline for AI coding agents."*
- **Short social:** *"Keep your tokens. Keep your work."*
- **On landing page:** *"One command. Every agent. Zero tokens wasted."*
- **In docs:** *"Your sandbox. Your memory. Your machine."*
- **Punchier alternative:** *"Stop your AI from flushing your budget."*

The primary tagline leads every external surface (landing page, README, GitHub description, X bio). The alternates serve context-specific uses.

**Anti-taglines (avoid):**

- Anything with "AI-powered," "intelligent," "smart" — DFMT is deliberate engineering, not another "smart" wrapper.
- Anything that starts with a verb like "Empowering," "Unleashing" — corporate voice we don't speak.
- "Context compression" alone — half the product. "Session memory" alone — other half.

---

## 4. Target Audience

**Primary:** Developers using AI coding agents (Claude Code, Cursor, Gemini CLI, VS Code Copilot, Codex CLI, OpenCode, Zed, Continue.dev, Windsurf) who:

- Feel the pain of context limits firsthand.
- Value local, inspectable, no-cloud tools.
- Run multiple projects, sometimes multiple agents in parallel.
- Care about software craftsmanship — they follow `#NOFORKANYMORE`-adjacent thinking even if they don't know the hashtag.

**Secondary:** DevRel / technical content creators who need reliable, demonstrable context-saving tools for tutorials and comparisons.

**Tertiary:** Engineering leaders at small/mid-size teams evaluating AI-assisted development workflows, wanting to measure and bound AI agent costs.

**Not the audience:**

- People looking for a hosted SaaS solution.
- People who want managed dashboards and team analytics.
- Enterprises needing SSO, audit logs, SOC 2 out of the box.

DFMT can grow into serving some of these later, but v1.0 is squarely for individual developers on their own machines.

---

## 5. Voice and Tone

### Voice (constant across all surfaces)

- **Direct.** No warm-up, no preamble. State the thing, then explain if needed.
- **Technical but not jargony.** We say "subprocess" not "execution context." We say "BM25 ranking" when relevant because it *is* the right term; we don't say "relevance algorithm" to dumb it down.
- **Confident without bragging.** "DFMT works on every major agent" not "DFMT is the best solution in the market." The claims stand on evidence, not adjectives.
- **Opinionated.** We have taken positions (MIT, per-project daemon, zero-dep, bundled parsers) and we stand behind them. Readers should feel the authorial weight.

### Tone (varies by surface)

| Surface | Tone | Example |
| --- | --- | --- |
| README | Practical, quickstart-first | "Install. Run `dfmt setup`. Done." |
| SPEC docs | Precise, engineering-register | "The daemon holds a LOCK_EX on `.dfmt/lock` at startup." |
| Landing page | Punchy, confident, short sentences | "Your context window. Protected." |
| X posts | Sharp, observational, sometimes sardonic | "Your agent just spent 56 KB of context to tell you a file exists. There is a better way." |
| Error messages | Clear, actionable, no apology | "Port 7777 in use. Set `transport.http.bind` in `.dfmt/config.yaml`." |
| Blog posts | Narrative, example-driven | "I ran a 3-hour coding session. Here's what the context window looked like." |

### Things we never do

- Use emoji in docs or commit messages. (Sparing use in X posts OK.)
- Add marketing prefixes: "Revolutionary," "Game-changing," "Next-generation."
- Over-qualify claims: "may help with," "could potentially," "might assist."
- Apologize for existing. DFMT doesn't ask permission.
- Compare to named competitors in negative framing. (Comparative content, if written, describes approaches without naming who does which — see previous doc revisions.)

### Things we always do

- Show numbers when we have them (KB saved, ms latency, % reduction).
- Link to the spec when a claim is load-bearing.
- Acknowledge limitations plainly. "Codex CLI has no hooks, so compliance is ~65% there."
- Ship our words. If DFMT says "static binary, any platform," `go install` had better work on Alpine.

---

## 6. Domain and Social Presence

### Primary domain

**`dfmt.dev`**

- Short, memorable, sibling-consistent with `dfmc.dev`.
- `.dev` signals developer-tool positioning.
- Registered via Google Domains (already standard for the portfolio).

### Secondary domains

- `dontfuckmytokens.com` — full-form redirect to `dfmt.dev`. Useful for conversational mentions ("go to dontfuckmytokens.com"). Lower priority.

### Social handles

- **GitHub org (already existing):** `ersinkoc` — DFMT lives under this as `github.com/ersinkoc/dfmt`. Consistent with the rest of the portfolio (Karadul, NothingDNS, Kervan, Argus, etc.).
- **X (Twitter):** No DFMT-specific handle. Ersin's personal X account handles all portfolio announcements. This is deliberate — the developer behind the work is part of the brand. A dedicated `@dfmt` account would feel corporate and cold.
- **Discord:** Portfolio-wide "ersinkoc" server, `#dfmt` channel. Shared community reduces fragmentation across portfolio projects.
- **Hacker News:** Ersin's personal HN account. DFMT launch is a Show HN.

### Email

- `info@dfmc.dev` — support/contact address. Reaches the same inbox as other portfolio products; volume will determine if routing ever matters.

---

## 7. Visual Identity

### Color palette

**Primary palette** — shared with DFMC for portfolio consistency:

| Color | Hex | Role |
| --- | --- | --- |
| **Obsidian** | `#1A1A2E` | Background, primary text on light. Serious, grounded. |
| **Alert** | `#EF4444` | Accent, emphasis, warnings. The "stop wasting tokens" red. |
| **Current** | `#06B6D4` | Interactive, links, active states. Cool cyan counterweight to red. |

**Secondary palette** — functional additions:

| Color | Hex | Role |
| --- | --- | --- |
| **Bone** | `#F5F5F4` | Background on light mode, text on dark mode. |
| **Slate** | `#64748B` | Muted text, secondary information, dividers. |
| **Savings** | `#10B981` | Used sparingly for "saved N tokens" stats. The positive-outcome green. |

**Usage rules:**

- Obsidian is the default bg of all branded surfaces.
- Alert (red) appears on the logomark, the primary CTA button, and on the stats that indicate waste ("45 KB of context wasted").
- Current (cyan) is for links, interactive chrome, and "DFMT savings" stats ("12 KB saved").
- Savings (green) is used only where numeric savings are reported, never as chrome. This discipline keeps the palette from fragmenting.

### Typography

**Headlines / Display:** `JetBrains Mono` — DFMT is a developer tool; the monospace face signals that immediately. Used for the logotype, section headers on landing page, code blocks in docs.

**Body:** `Inter` — neutral, readable at small sizes, sits next to monospace without clashing. Used for all body text in docs and landing page.

**Code / Terminal:** `JetBrains Mono` — unified face for code blocks.

Both fonts are freely available (SIL Open Font License / Apache-2.0) and embedded into the landing page via Google Fonts or self-hosted. No licensing concerns.

### Logomark

**Concept:** A stylized `T` — the final letter of the acronym — formed by the intersection of a horizontal "budget line" and a vertical "cut stroke." The cut stroke is rendered in Alert red, implying "here is where we stop the waste." The horizontal line is in Current cyan, implying the preserved, protected token budget.

**Structure:**

- Square canvas, 1:1 aspect.
- Obsidian background (or transparent on light themes).
- Bold geometric `T` — not traditional typographic T, but a construction: two rectangles meeting at 90°.
- Horizontal bar: 70% canvas width, 10% canvas height, Current cyan.
- Vertical stroke: 10% canvas width, 60% canvas height, Alert red, centered under the horizontal, starting from the bottom of the horizontal bar.
- Subtle glow around the vertical stroke (optional, for hero contexts): 2% Alert red radial fade.

**Logotype:**

"DFMT" set in JetBrains Mono ExtraBold, all caps, tracking +50. Placed directly to the right of the logomark for horizontal lockups, or directly below for vertical/square lockups.

### Nano-Banana 2 generation brief

When generating logo renders, infographics, or supporting visuals via nano-banana-pro:

**Master prompt template (for logomark):**

```
Minimalist geometric logo on deep navy obsidian background (#1A1A2E).
A bold, square-proportioned stylized letter "T" constructed from two
solid rectangles meeting at a right angle. The horizontal bar of the T
is rendered in cool cyan (#06B6D4), spanning 70% of the canvas width.
The vertical stroke of the T is rendered in alert red (#EF4444),
descending from the center of the horizontal bar. Subtle red glow
behind the vertical stroke. No other elements. Sharp, confident,
engineered feel. 1:1 aspect ratio. Vector-style flat design.
No text, no wordmark, no decorative flourishes.
```

**Portfolio consistency directive** (append to all branded visuals):

```
Visual language: consistent with the ersinkoc portfolio (Karadul,
NothingDNS, DFMC, Argus). Deep navy primary, red accent for stop/halt
semantics, cyan for flow/active semantics. Clean, engineered, not
corporate. Typography: JetBrains Mono. No stock illustrations, no
abstract geometric "tech" decoration, no gradients outside of
narrow glow effects.
```

### Iconography

Line-art style icons, 1.5px stroke weight, matched to logomark geometry. Used on landing-page feature grids and README badges.

Standard icons for DFMT's core functions:

- **Sandbox:** a box with an arrow exiting through a checkpoint.
- **Memory:** a folded ribbon/timeline.
- **Multi-agent:** a central hub with five spokes.
- **Local-first:** a house silhouette.
- **Zero-dep:** a self-contained circle with no external connections.

---

## 8. Landing Page Structure

`dfmt.dev` — single page, scroll-driven. Sections in order:

1. **Hero.** Full-viewport. Obsidian background. Logomark centered, logotype below. Primary tagline in JetBrains Mono. Install one-liner as a copy-button snippet. Two CTA buttons: "View on GitHub" and "Read the spec."

2. **Problem visualization.** Two-column: left column shows a session with native tools (context bar filling red), right column shows the same session with DFMT (context bar staying flat). Numbers: "Session A: 62% context used after 30 min. Session B: 14% context used after 30 min."

3. **How it works.** Three cards, side by side.
   - Card 1: **Sandbox** — exec, read, fetch with intent filtering.
   - Card 2: **Memory** — session state captured, compaction-proof snapshots.
   - Card 3: **Setup** — one command, every agent.
   Each card has the matching icon and a three-line description.

4. **Install.** Code block with tabs for each platform (macOS, Linux, Windows, `go install`). Plus the two-line quickstart (`dfmt init && dfmt setup`).

5. **Agent grid.** Nine agent logos in a 3×3 grid with status chips ("full," "high," "high," "high," "partial," etc.). Hover reveals exact config paths and restart requirements. Link to AGENT-INTEGRATION.md for details.

6. **Benchmarks.** Four stat cards: "98% context reduction on tool output," "session continuity across unlimited compactions," "<5 ms search p99," "8 MB single binary." Small footnote with methodology link.

7. **Philosophy.** Three-paragraph section titled "Why this shape?" — links to ADRs as "read more." Positions DFMT's stdlib-only, per-project-daemon, MIT-licensed architecture as intentional.

8. **Footer.** GitHub link, Discord link, Ersin's X handle, email, license, "Part of the ersinkoc portfolio" with small logo grid of sibling projects.

No testimonials section in v1. No "trusted by" logos. No newsletter signup. Keep it sharp.

---

## 9. Launch Materials

### X / Twitter — Launch thread

A 6-tweet thread, posted from Ersin's personal account. Structure:

**Tweet 1 (hook):**

> Your AI coding agent just spent 56 KB of context to tell you a file exists.
> 45 minutes into your session, 40% of the window is gone to stale tool output.
> Then it compacts. Your tasks, decisions, and last prompt — vanished.
>
> Today I'm releasing DFMT. 🧵

**Tweet 2 (what):**

> DFMT is a local daemon that sits between your AI agent and its tools.
>
> Shell commands, file reads, URL fetches go through DFMT's sandbox. Raw output stays in a local index. Your context sees summaries and intent-matched excerpts.
>
> Session state survives every compaction.

**Tweet 3 (how — sandbox):**

> Instead of `Bash("cat big.log | grep ERROR")` flooding your context with 40 KB...
>
> You call `dfmt.exec(code: "cat big.log", intent: "recent errors")`.
>
> Same execution. The intent argument makes DFMT return only matching lines plus a vocabulary of other interesting terms.

**Tweet 4 (how — memory):**

> When the conversation compacts, DFMT has been tracking: files you edited, tasks, user decisions, git ops, errors.
>
> It rebuilds a budget-capped snapshot and hands it back to the agent on resume.
>
> Your model picks up where you left off without asking.

**Tweet 5 (setup):**

> Works on Claude Code, Cursor, Codex, Gemini CLI, Copilot, OpenCode, Zed, Continue.dev, Windsurf.
>
> `dfmt setup` detects every installed agent and configures it.
>
> One command. Every agent. No per-tool manual config.

**Tweet 6 (CTA):**

> DFMT is MIT-licensed, zero external runtime deps, one 8 MB static binary.
>
> Local. Inspectable. No telemetry.
>
> dfmt.dev

**Notes for the thread:**

- No emoji other than the single 🧵 in tweet 1.
- Each tweet stands alone — any one of them, forwarded without context, should make sense.
- Tweet 3 includes a code-like snippet but avoids full code blocks (they render poorly on X). Use angle brackets sparingly.
- Schedule: Tuesday or Wednesday morning US Eastern. Post volume lowest, developer engagement highest.

### Hacker News — Show HN

Title: `Show HN: DFMT – Stop AI coding agents from wasting your context window`

Body:

> Hey HN. I'm Ersin, solo maintainer.
>
> DFMT is a local daemon I've been building because every AI coding session I run goes the same way: an hour in, half my context window is consumed by stale tool output, a compaction fires, my working state vanishes. My model then asks me questions it already asked.
>
> DFMT sits between any AI coding agent and its tools. Shell commands, file reads, URL fetches go through a sandbox that keeps the raw output out of the context window — it gets indexed locally and returned as intent-matched excerpts. Separately, session state (files edited, decisions, tasks, git ops) gets captured into a local journal and rebuilt as a compact snapshot when the conversation resets.
>
> Written in Go, ships as a single 8 MB binary. Works on Claude Code, Cursor, Codex CLI, Gemini CLI, VS Code Copilot, OpenCode, Zed, Continue.dev, Windsurf — `dfmt setup` detects installed agents and configures each. MIT-licensed, no cloud, no telemetry.
>
> The spec and nine ADRs are public for anyone who wants to see why it's shaped the way it is.
>
> Happy to take feedback / criticism. dfmt.dev · github.com/ersinkoc/dfmt

**Posting rules:**

- Post between 8-10 AM US Eastern on a weekday.
- Respond to every comment in the first 4 hours.
- Never be defensive; treat every criticism as signal.
- Don't ask for upvotes; don't coordinate.

### Blog post — companion piece

A single longer article, published on `dfmt.dev/blog/` and cross-posted to Medium / dev.to, titled:

> **Why your AI coding agent forgets what you just told it**

Structure:

1. Opening scene — a real session where the model drifts.
2. The two failure modes — tool bloat + state loss.
3. Measurement — actual numbers from real sessions.
4. DFMT's approach — sandbox tools + session journal.
5. Why local, why single binary, why per-project daemon.
6. The pitch for trying it.

~1,800 words. Written in Ersin's voice. Link-rich (ADRs, spec, GitHub).

---

## 10. Elevator Pitches

**30-second version:**

> DFMT is a local daemon for AI coding agents. Your agent's shell, file-read, and fetch calls go through it — the raw output stays in a local index, the agent sees summaries. And when the conversation compacts, DFMT hands back a snapshot of what was happening, so the agent doesn't restart from zero. Works on Claude Code, Cursor, Codex, Gemini, Copilot, and more. MIT-licensed, single binary, no cloud.

**60-second version:**

> Every AI coding session suffers two problems. Tool outputs — shell commands, file reads, web fetches — flood the context window with raw data. Then when the context fills up and compacts, the agent forgets what it was working on.
>
> DFMT is a local daemon that solves both. It runs in the background per project. When your agent needs to run a command or read a file, it goes through DFMT's sandbox instead of the native tool. Raw output stays in a local ephemeral index; your context sees a summary plus whatever your agent specifically asked for.
>
> Separately, DFMT tracks session events — files edited, user decisions, errors, git commits — and when the conversation resets, builds a compact snapshot of all that state. The agent picks up where it left off.
>
> One command — `dfmt setup` — detects every installed AI coding agent on your machine and configures it. Claude Code, Cursor, Codex CLI, Gemini CLI, VS Code Copilot, OpenCode, Zed, Continue.dev, Windsurf. MIT-licensed. Single 8 MB binary. Zero cloud, zero telemetry.

**2-minute version** (for deeper conversations):

The 60-second version plus:

- How the intent-filtering works (user provides intent, DFMT runs BM25 search against indexed chunks, returns matches + vocabulary).
- The architecture (per-project daemon, auto-start, idle-exit).
- Why stdlib-only (ADR-0004).
- Why bundled parsers and tokenizers (ADR-0008).
- Concrete numbers (56 KB → 299 B on a Playwright snapshot, 60 KB → 1.1 KB on a GitHub issue list).
- How a developer actually uses it day-to-day (`dfmt setup`, restart agents, work normally).

---

## 11. Portfolio Context

DFMT sits within Ersin's ECOSTACK open-source portfolio. Strategic relationship to other projects:

| Project | Role | Relationship to DFMT |
| --- | --- | --- |
| **DFMC** — Don't Fuck My Code | Code-quality companion for AI-assisted development | **Sibling.** Same voice, same palette, cross-promote. |
| **DFMS** — Don't Fuck My Setup | (reserved) Config/ergonomics protection | Future sibling, not yet built. |
| **Karadul** | Mesh VPN (Tailscale alternative) | **Portfolio neighbor.** Different domain; shares distribution channels. |
| **NothingDNS** | DNS server | Portfolio neighbor. |
| **OpenLoadBalancer** | Load balancer | Portfolio neighbor. |
| Others | ~18 open-source infra tools | Portfolio neighbors. |

Cross-promotion strategy:

- DFMT README mentions DFMC in the "Related projects" footer. DFMC does the reverse.
- Launch announcement names the portfolio explicitly: "Part of the `ersinkoc` open-source portfolio."
- Landing page footer has a 6-project logo strip linking to sibling products.
- X thread can reference "second sibling after DFMC" to activate existing DFMC followers.

Brand tension to watch: DFMC and DFMT share color palette and voice deliberately. We should *not* share logomarks — each product needs a unique mark. DFMC's mark is its own; DFMT's mark (described in §7) is distinct.

---

## 12. Launch Checklist

Before public announcement, verify:

- [ ] v1.0.0 tagged and released on GitHub with signed binaries for all 7 target triples
- [ ] `dfmt setup` tested end-to-end on all 9 supported agents (Tier 1+2+3 smoke tests)
- [ ] `install.sh` tested on macOS (arm64 and amd64), Ubuntu, Alpine, Windows
- [ ] Homebrew tap and Scoop bucket published and functional
- [ ] `dfmt.dev` landing page deployed with all sections live
- [ ] BLOG post "Why your AI coding agent forgets..." drafted and reviewed
- [ ] Hacker News Show HN post drafted, ready to submit
- [ ] X thread drafted, pre-loaded in a scheduler
- [ ] `docs/adr/` all nine ADRs present and indexed
- [ ] `README.md` with install per platform and `dfmt setup` demo
- [ ] `LICENSE-THIRD-PARTY.md` generated and current
- [ ] Discord `#dfmt` channel created, pinned welcome message ready
- [ ] DFMC cross-reference updated to mention DFMT in its "Related" section
- [ ] Support email `info@dfmt.dev` routing verified
- [ ] `git` repo description set on GitHub with primary tagline
- [ ] GitHub repo topics set: `mcp`, `ai-coding`, `claude-code`, `cursor`, `context-window`, `developer-tools`, `go`, `mit-license`

Pre-launch dogfood:

- [ ] DFMT used in DFMT's own development for at least two weeks
- [ ] At least one other portfolio project (Karadul or NothingDNS) migrated to use DFMT in its dev loop
- [ ] Personal Ersin X timeline has background posts about context-window problems for 2-3 weeks before launch (warm audience)

---

## 13. Anti-patterns — Specific things we will not do

**In branding:**

- No "AI" in the name or tagline. DFMT serves AI agents; it isn't itself an AI product.
- No "enterprise-grade" language. DFMT is individual-developer-grade.
- No "powered by" banners from infrastructure providers. DFMT has no infrastructure providers.
- No trademark symbols. DFMT is MIT and unregistered; the brand protection comes from reputation, not registration.

**In communications:**

- No generic "thanks for the feedback!" on issues. Real engagement or no engagement.
- No drip-feed release announcements stretching over weeks. One launch, done.
- No "coming soon" teasers. Ship first, announce second.
- No vague roadmap promises. Everything post-v1.0 that matters is in the spec or an ADR.

**In visuals:**

- No stock photography. Ever.
- No 3D renderings or gradient-heavy "hero shots" pretending to be abstract representations of "AI."
- No illustrations of "happy developers" at laptops. DFMT's visual language is typographic and geometric, not representational.

---

## 14. Metrics — What we track, what we don't

**We track publicly:**

- GitHub stars, forks, issues opened/closed (public GitHub metrics).
- Download counts on releases (public GitHub releases API).
- Hacker News post performance (score, comments).
- X engagement on launch-related posts.

**We do not track:**

- Per-user telemetry from DFMT itself. Not opt-in. Not opt-out. Not shipped.
- Who installed DFMT, which agents they configure, how they use it. Zero server-side visibility by design.
- Any metric that would require data to leave a user's machine.

**Success criteria for launch week:**

- 500+ GitHub stars — indicates initial resonance.
- 50+ HN comments — indicates discussion, even if mixed.
- 3+ unsolicited blog posts / comparative reviews — indicates the wider community is engaging.
- At least one "I tried it and…" post with real session data.

**Success criteria for v1.1 (3 months post-launch):**

- 1000+ stars.
- 10+ community contributors (issues resolved, PRs merged).
- Every supported agent has at least one user reporting successful use.
- At least one agent's maintainer team has taken notice (filed an issue, mentioned DFMT in their docs, etc.).

These are targets, not guarantees. A slower trajectory doesn't invalidate the product — it just means launch communications need more iterations.

---

## 15. Open Brand Questions

These are deliberately left unresolved for post-launch iteration based on signal:

- Whether to commission a professional logomark / brand package after launch, or keep the self-designed mark. Depends on traction.
- Whether to run a podcast / video series about AI coding agent context management. Depends on bandwidth.
- Whether to attend / speak at conferences with DFMT as a focus topic. Depends on invitation volume and venue fit.
- Whether to produce branded merchandise (stickers, t-shirts). Only if asked repeatedly.

---

*End of brand guidelines.*
