# `ore demo` transcript

Recorded on 2026-10-19T01:47Z on macOS arm64 with Docker 29.1.2, git 2.51.0. The agent is the deterministic demo script; swap in Claude Code with `ORE_AGENT_CMD`.

```

1. one binary is serving git, API, UI and the loop at http://127.0.0.1:65015 (autonomy level L4 "ship")
   created demo/hello  (git clone http://127.0.0.1:65015/demo/hello.git)

2. commit a sample app through the API: no clone, no working copy (server-side commit)
   refs/heads/main -> 3413f50958

3. the signal: the pipeline on main is red
   pipeline prun_000038AHTYDG68DZFSF4: failed
   issue #1 filed: Greeting should say Hello, Ore!

4. the loop: plan -> change -> verify -> land -> deploy -> observe (agent: demo-script)
   agent run arun_000038AHTYV159MCTE7J: completed (last step: observe)
   01:47:33.779 [ore] step: plan
   01:47:33.800 [ore] step: change
   01:47:34.075 [change] pushed ore/agent/issue-1-MCTE7J
   01:47:34.075 [ore] step: verify
   01:47:34.093 [verify] change #1 (I8e6d71e6f0841db7c77beab0561b9d4e4e937978) base 3413f509 head cd635bb6
   01:47:40.422 [verify] pipeline prun_000038AHTYVMT038ZP4B: succeeded
   01:47:41.077 [verify] preview at http://localhost:55002
   01:47:41.077 [ore] step: land
   01:47:41.128 [land] merged into refs/heads/main as d5a4205c
   01:47:41.133 [ore] step: deploy
   01:47:41.496 [deploy] production at http://localhost:55003
   01:47:41.496 [ore] step: observe

5. the result
   change #1 merged: Greeting should say Hello, Ore! (change-id I8e6d71e6f08, provenance {"agent_id":"demo-script","author_kind":"agent","session_id":"arun_000038AHTYV159MCTE7J"})
   deployment dep_000038AHTZ9CXVGFS52Z production succeeded http://localhost:55003
   GET http://localhost:55003 -> Hello, Ore!
   deployment dep_000038AHTZ8194MNXPHY preview    succeeded http://localhost:55002
   ledger: 29 events
       1  io.ore.org.created                     orgs/demo
       2  io.ore.repository.created              demo/hello
       3  io.ore.ref.updated                     demo/hello/refs/heads/main
       4  io.ore.pipeline.run.queued             demo/hello/runs/prun_000038AHTYDG68DZFSF4
       5  io.ore.pipeline.run.started            demo/hello/runs/prun_000038AHTYDG68DZFSF4
       6  io.ore.pipeline.run.finished           demo/hello/runs/prun_000038AHTYDG68DZFSF4
       7  io.ore.issue.opened                    demo/hello/issues/1
       8  io.ore.agent.session.started           demo/hello/agent/arun_000038AHTYV159MCTE7J
       9  io.ore.agent.session.step              demo/hello/agent/arun_000038AHTYV159MCTE7J
      10  io.ore.agent.session.step              demo/hello/agent/arun_000038AHTYV159MCTE7J
      11  io.ore.ref.updated                     demo/hello/refs/heads/ore/agent/issue-1-MCTE7J
      12  io.ore.agent.session.step              demo/hello/agent/arun_000038AHTYV159MCTE7J
      13  io.ore.change.opened                   demo/hello/changes/1
      14  io.ore.pipeline.run.queued             demo/hello/runs/prun_000038AHTYVMT038ZP4B
      15  io.ore.pipeline.run.started            demo/hello/runs/prun_000038AHTYVMT038ZP4B
      16  io.ore.pipeline.run.finished           demo/hello/runs/prun_000038AHTYVMT038ZP4B
      17  io.ore.deployment.requested            demo/hello/deployments/dep_000038AHTZ8194MNXPHY
      18  io.ore.deployment.started              demo/hello/deployments/dep_000038AHTZ8194MNXPHY
      19  io.ore.deployment.succeeded            demo/hello/deployments/dep_000038AHTZ8194MNXPHY
      20  io.ore.agent.session.step              demo/hello/agent/arun_000038AHTYV159MCTE7J
      21  io.ore.ref.updated                     demo/hello/refs/heads/main
      22  io.ore.change.merged                   demo/hello/changes/1
      23  io.ore.agent.session.step              demo/hello/agent/arun_000038AHTYV159MCTE7J
      24  io.ore.deployment.requested            demo/hello/deployments/dep_000038AHTZ9CXVGFS52Z
      25  io.ore.deployment.started              demo/hello/deployments/dep_000038AHTZ9CXVGFS52Z
      26  io.ore.deployment.succeeded            demo/hello/deployments/dep_000038AHTZ9CXVGFS52Z
      27  io.ore.agent.session.step              demo/hello/agent/arun_000038AHTYV159MCTE7J
      28  io.ore.issue.closed                    demo/hello/issues/1
      29  io.ore.agent.session.completed         demo/hello/agent/arun_000038AHTYV159MCTE7J

done in 14.762s.  UI: http://127.0.0.1:65015/login?token=demo-65015
```
