# Web dependency security review — 2026-08-05

This record covers the public npm advisories tracked by issue #5 against
`web/package-lock.json` at commit `8f04bee`. It contains no private report or
exploit material.

## Reachability and disposition

| Advisory | Baseline surface | Reachability in mem Web | Disposition |
| --- | --- | --- | --- |
| [GHSA-2j2x-hqr9-3h42](https://github.com/advisories/GHSA-2j2x-hqr9-3h42) | React Router protocol-relative redirects | No `redirect()` call exists. Application navigation targets are fixed root-relative routes or identifiers placed below a fixed route prefix. | Removed the affected 6.30.3 graph. |
| [GHSA-wrjc-x8rr-h8h6](https://github.com/advisories/GHSA-wrjc-x8rr-h8h6) | `Link` and `useNavigate` destination handling | These APIs are widely used, so the dependency path is reachable. Dynamic folder, task, checkpoint, memory, and file destinations remain below fixed local prefixes; folder/task/checkpoint/memory segments are encoded before routing. Upgrading is still required as defense in depth and for future call sites. | Upgraded beyond the affected range. |
| [GHSA-337j-9hxr-rhxg](https://github.com/advisories/GHSA-337j-9hxr-rhxg) | SSR hydration error deserialization | Not reachable: the Vite client starts with `createRoot`; it has no SSR hydration or server-serialized router errors. | Removed the affected 6.30.3 graph. |
| [GHSA-jjmj-jmhj-qwj2](https://github.com/advisories/GHSA-jjmj-jmhj-qwj2) | `react-router-dom` redirect handling | The package and its navigation exports were present. No direct external destination is passed by current source, but retaining the affected compatibility package leaves future call sites exposed. | Removed `react-router-dom` and migrated to the v8 package boundary. |
| [GHSA-qwww-vcr4-c8h2](https://github.com/advisories/GHSA-qwww-vcr4-c8h2) | Unstable React Server Components actions | Not reachable: mem Web has no React Server Components, route actions, or framework-mode server runtime. This advisory appeared while evaluating the v7 upgrade and made v7 unsuitable as the fixed target. | Upgraded to patched React Router 8.3.0. |
| [GHSA-mh99-v99m-4gvg](https://github.com/advisories/GHSA-mh99-v99m-4gvg) and [GHSA-rgw5-rvv9-x895](https://github.com/advisories/GHSA-rgw5-rvv9-x895) | Transitive `brace-expansion` use by lint/build tooling | Not shipped in the production graph. Exposure is limited to repository-controlled lint/build glob inputs in developer and CI environments. | Refreshed the lockfile to patched 1.1.18 and 2.1.4 releases. |

## Result and continuing gate

- `npm audit --omit=dev --audit-level=moderate`: zero vulnerabilities.
- `npm audit --audit-level=high`: zero vulnerabilities.
- No advisory exception, risk owner, or expiry is required.
- CI runs both commands through `npm run audit` without `continue-on-error`.

The version upgrade is behavior-checked by the Web lint, type-check, production
build, and browser acceptance suites. A future advisory is evaluated against
its affected surface; the audit result alone does not establish reachability.
