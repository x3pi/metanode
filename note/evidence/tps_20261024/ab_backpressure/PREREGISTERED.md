# A/B pre-registration (written before any measurement)
- Base commit: 44bedbcbbe5eadd47897421e03bcfe37e511a39b  (dev, working tree clean apart from the B patch)
- A = unchanged proposer; B = proposer.rs rate-term gated on go_lag > GO_LAG_COMFORT_ZONE (patch: proposer.patch)
- sha256 A simple_chain: 5a7673f28b5ef438f59e5a16596788715957a80a957d1c46a9e63c15ff054dae
- sha256 B simple_chain: 0e0bcab3ed7c355d17ae44b2dc9519b8816e5706d0f165dd8c382d5dc7e59829
- sha256 tool (both): 0b86a33154542edead982824328f95b014343162d9b70be512cd23eda0e6c6f3
- Method: run_tps_counted.py, sustained, --max-txs 840000, fresh 4-validator cluster per run, interleaved A,B,A,B,A,B, same session.
- Metric: tps_end_to_end from the tool's counted report. Hypothesis: B >= A. Report whatever comes out.
