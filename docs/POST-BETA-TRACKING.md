# Post-beta tracking

September 7, 2026. The public 0.2.1-beta delivery gates B1-B4 are complete.
Prioritize adopter feedback and reproducible defects in the supported core, then
continue the architecture and qualification backlog. Preserve documented beta
syntax. No new release or milestone closure is implied by this tracking update.

## Milestones

GitHub milestones mirror [repository acceptance gates](MILESTONES.md). R0-R2
are in progress; R3-R8 remain planned, with some groundwork already delivered.
Planned milestones may have no implementation issues yet; their work packages
remain in MILESTONES.md. There are no promised due dates.

| Gate | GitHub tracking |
| --- | --- |
| R0 | [Reproducible baseline and qualification harness](https://github.com/PLN/winunitd/milestone/1) |
| R1 | [Ownership, output, and failure containment](https://github.com/PLN/winunitd/milestone/2) |
| R2 | [Authoritative lifecycle coordinator](https://github.com/PLN/winunitd/milestone/3) |
| R3 | [Unit semantics, compatibility, and health](https://github.com/PLN/winunitd/milestone/4) |
| R4 | [Windows identities and manager qualification](https://github.com/PLN/winunitd/milestone/5) |
| R5 | [Durable timers and operational diagnostics](https://github.com/PLN/winunitd/milestone/6) |
| R6 | [Fully qualified serviceable MSI](https://github.com/PLN/winunitd/milestone/7) |
| R7 | [Pilot replacement and soak](https://github.com/PLN/winunitd/milestone/8) |
| R8 | [Verified signed installation release](https://github.com/PLN/winunitd/milestone/9) |

## Focused follow-up

[Test hardening #40](https://github.com/PLN/winunitd/issues/40) belongs to R0.
The [test audit](TEST-HARDENING.md) records coverage and evidence boundaries.
Its completion does not qualify experimental timers/resource controls for beta.
The following issues make the next work visible without replacing the full gates.

| Gate | Work |
| --- | --- |
| R0 | [R0.4: Complete repeatable Windows qualification harness](https://github.com/PLN/winunitd/issues/93) |
| R1 | [R1.1-R1.3: Finish ownership and cleanup qualification evidence](https://github.com/PLN/winunitd/issues/94) |
| R1 | [R1.4: Qualify journal fairness under aggregate overload](https://github.com/PLN/winunitd/issues/95) |
| R2 | [R2.1-R2.2: Migrate lifecycle decisions to one coordinator](https://github.com/PLN/winunitd/issues/96) |
| R2 | [R2.3: Bound admission and reserve lifecycle completion capacity](https://github.com/PLN/winunitd/issues/97) |
| R2 | [R2.4: Give accepted operations internal deadlines and cancellation ownership](https://github.com/PLN/winunitd/issues/98) |
| R5 | [R5.1: Make persistent timer state atomic and failures observable](https://github.com/PLN/winunitd/issues/99) |

The next lifecycle implementation should be a bounded R2.4 operation-lifetime
slice, coordinated with the R2.1-R2.2 migration. Graceful stop, semantic migration,
SYSTEM/user qualification, full MSI servicing and signing keep their separate
gates. Successful beta installation and maintenance tests do not close them.
