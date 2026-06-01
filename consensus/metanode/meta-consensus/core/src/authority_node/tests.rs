use super::*;

    #![allow(non_snake_case)]

    use std::{
        collections::{BTreeMap, BTreeSet},
        sync::Arc,
        time::Duration,
    };

    use consensus_config::{local_committee_and_keys, Parameters};
    use meta_protocol_config::ProtocolConfig;
    use mysten_metrics::monitored_mpsc::UnboundedReceiver;
    
    use prometheus::Registry;
    use rstest::rstest;
    use tempfile::TempDir;
    use tokio::time::{sleep, timeout};
    

    use super::*;
    use crate::{
        block::{BlockAPI as _, CertifiedBlocksOutput, GENESIS_ROUND},
        transaction::NoopTransactionVerifier,
        CommittedSubDag,
    };

    #[rstest]
    #[tokio::test]
    async fn test_authority_start_and_stop(
        #[values(NetworkType::Tonic)] network_type: NetworkType,
    ) {
        let (committee, keypairs) = local_committee_and_keys(0, vec![1]);
        let registry = Registry::new();

        let temp_dir = TempDir::new().unwrap();
        let parameters = Parameters {
            db_path: temp_dir.keep(),
            ..Default::default()
        };
        let txn_verifier = NoopTransactionVerifier {};

        let own_index = committee.to_authority_index(0).unwrap();
        let protocol_keypair = keypairs[own_index].1.clone();
        let network_keypair = keypairs[own_index].0.clone();

        let (commit_consumer, _, _) = CommitConsumerArgs::new(0, 0, [0; 32], 0);

        let authority = ConsensusAuthority::start(
            network_type,
            0,
            0,
            0,
            own_index,
            committee,
            parameters,
            ProtocolConfig::get_for_max_version_UNSAFE(),
            protocol_keypair,
            network_keypair,
            Arc::new(Clock::default()),
            Arc::new(txn_verifier),
            commit_consumer,
            registry,
            0,
            None,
            None, // legacy_store_manager
            crate::coordination_hub::ConsensusCoordinationHub::new_for_testing(),
        )
        .await;

        assert_eq!(authority.context().own_index, own_index);
        assert_eq!(authority.context().committee.epoch(), 0);
        assert_eq!(authority.context().committee.size(), 1);

        authority.stop().await;
    }

    // TODO: build AuthorityFixture.
    #[rstest]
    #[tokio::test(flavor = "current_thread")]
    #[ignore]
    async fn test_authority_committee(
        #[values(NetworkType::Tonic)] network_type: NetworkType,
        #[values(5, 10)] gc_depth: u32,
    ) {
        // // telemetry_subscribers::init_for_testing();
        let _db_registry = Registry::new();
        // DBMetrics::init(RegistryService::new(db_registry));

        const NUM_OF_AUTHORITIES: usize = 4;
        let (committee, keypairs) = local_committee_and_keys(0, [1; NUM_OF_AUTHORITIES].to_vec());
        let mut protocol_config = ProtocolConfig::get_for_max_version_UNSAFE();
        protocol_config.set_consensus_gc_depth_for_testing(gc_depth);

        let temp_dirs = (0..NUM_OF_AUTHORITIES)
            .map(|_| TempDir::new().unwrap())
            .collect::<Vec<_>>();

        let mut commit_receivers = Vec::with_capacity(committee.size());
        let mut block_receivers = Vec::with_capacity(committee.size());
        let mut authorities = Vec::with_capacity(committee.size());
        let mut boot_counters = [0; NUM_OF_AUTHORITIES];

        for (index, _authority_info) in committee.authorities() {
            let (authority, commit_receiver, block_receiver) = make_authority(
                index,
                &temp_dirs[index.value()],
                committee.clone(),
                keypairs.clone(),
                network_type,
                boot_counters[index],
                protocol_config.clone(),
            )
            .await;
            boot_counters[index] += 1;
            commit_receivers.push(commit_receiver);
            block_receivers.push(block_receiver);
            authorities.push(authority);
        }

        const NUM_TRANSACTIONS: u8 = 15;
        let mut submitted_transactions = BTreeSet::<Vec<u8>>::new();
        for i in 0..NUM_TRANSACTIONS {
            let txn = vec![i; 16];
            submitted_transactions.insert(txn.clone());
            authorities[i as usize % authorities.len()]
                .transaction_client()
                .submit(vec![txn])
                .await
                .unwrap();
        }

        for receiver in &mut commit_receivers {
            let mut expected_transactions = submitted_transactions.clone();
            loop {
                let committed_subdag =
                    tokio::time::timeout(Duration::from_secs(1), receiver.recv())
                        .await
                        .unwrap()
                        .unwrap();
                for b in committed_subdag.blocks {
                    for txn in b.transactions().iter().map(|t| t.data().to_vec()) {
                        assert!(
                            expected_transactions.remove(&txn),
                            "Transaction not submitted or already seen: {:?}",
                            txn
                        );
                    }
                }
                assert_eq!(committed_subdag.reputation_scores_desc, vec![]);
                if expected_transactions.is_empty() {
                    break;
                }
            }
        }

        // Stop authority 1.
        let index = committee.to_authority_index(1).unwrap();
        authorities.remove(index.value()).stop().await;
        sleep(Duration::from_millis(500)).await;
        sleep(Duration::from_secs(10)).await;

        // Restart authority 1 and let it run.
        let (authority, commit_receiver, block_receiver) = make_authority(
            index,
            &temp_dirs[index.value()],
            committee.clone(),
            keypairs.clone(),
            network_type,
            boot_counters[index],
            protocol_config.clone(),
        )
        .await;
        boot_counters[index] += 1;
        commit_receivers[index] = commit_receiver;
        block_receivers[index] = block_receiver;
        authorities.insert(index.value(), authority);
        sleep(Duration::from_secs(10)).await;

        // Stop all authorities and exit.
        for authority in authorities {
            authority.stop().await;
        }
    }

    #[rstest]
    #[tokio::test(flavor = "current_thread")]
    #[ignore]
    async fn test_small_committee(
        #[values(NetworkType::Tonic)] network_type: NetworkType,
        #[values(1, 2, 3)] num_authorities: usize,
    ) {
        // // telemetry_subscribers::init_for_testing();
        let _db_registry = Registry::new();
        // DBMetrics::init(RegistryService::new(db_registry));

        let (committee, keypairs) = local_committee_and_keys(0, vec![1; num_authorities]);
        let protocol_config: ProtocolConfig = ProtocolConfig::get_for_max_version_UNSAFE();

        let temp_dirs = (0..num_authorities)
            .map(|_| TempDir::new().unwrap())
            .collect::<Vec<_>>();

        let mut output_receivers = Vec::with_capacity(committee.size());
        let mut authorities: Vec<ConsensusAuthority> = Vec::with_capacity(committee.size());
        let mut boot_counters = vec![0; num_authorities];

        for (index, _authority_info) in committee.authorities() {
            let (authority, commit_receiver, _block_receiver) = make_authority(
                index,
                &temp_dirs[index.value()],
                committee.clone(),
                keypairs.clone(),
                network_type,
                boot_counters[index],
                protocol_config.clone(),
            )
            .await;
            boot_counters[index] += 1;
            output_receivers.push(commit_receiver);
            authorities.push(authority);
        }

        const NUM_TRANSACTIONS: u8 = 15;
        let mut submitted_transactions = BTreeSet::<Vec<u8>>::new();
        for i in 0..NUM_TRANSACTIONS {
            let txn = vec![i; 16];
            submitted_transactions.insert(txn.clone());
            authorities[i as usize % authorities.len()]
                .transaction_client()
                .submit(vec![txn])
                .await
                .unwrap();
        }

        for receiver in &mut output_receivers {
            let mut expected_transactions = submitted_transactions.clone();
            loop {
                let committed_subdag =
                    tokio::time::timeout(Duration::from_secs(1), receiver.recv())
                        .await
                        .unwrap()
                        .unwrap();
                for b in committed_subdag.blocks {
                    for txn in b.transactions().iter().map(|t| t.data().to_vec()) {
                        assert!(
                            expected_transactions.remove(&txn),
                            "Transaction not submitted or already seen: {:?}",
                            txn
                        );
                    }
                }
                assert_eq!(committed_subdag.reputation_scores_desc, vec![]);
                if expected_transactions.is_empty() {
                    break;
                }
            }
        }

        // Stop authority 0.
        let index = committee.to_authority_index(0).unwrap();
        authorities.remove(index.value()).stop().await;
        sleep(Duration::from_millis(500)).await;
        sleep(Duration::from_secs(10)).await;

        // Restart authority 0 and let it run.
        let (authority, commit_receiver, _block_receiver) = make_authority(
            index,
            &temp_dirs[index.value()],
            committee.clone(),
            keypairs.clone(),
            network_type,
            boot_counters[index],
            protocol_config.clone(),
        )
        .await;
        boot_counters[index] += 1;
        output_receivers[index] = commit_receiver;
        authorities.insert(index.value(), authority);
        sleep(Duration::from_secs(10)).await;

        // Stop all authorities and exit.
        for authority in authorities {
            authority.stop().await;
        }
    }

    #[rstest]
    #[tokio::test(flavor = "current_thread")]
    #[ignore]
    async fn test_amnesia_recovery_success(#[values(5, 10)] gc_depth: u32) {
        // // telemetry_subscribers::init_for_testing();
        let _db_registry = Registry::new();
        // DBMetrics::init(RegistryService::new(db_registry));

        const NUM_OF_AUTHORITIES: usize = 4;
        let (committee, keypairs) = local_committee_and_keys(0, [1; NUM_OF_AUTHORITIES].to_vec());
        let mut commit_receivers = vec![];
        let mut block_receivers = vec![];
        let mut authorities = BTreeMap::new();
        let mut temp_dirs = BTreeMap::new();
        let mut boot_counters = [0; NUM_OF_AUTHORITIES];

        let mut protocol_config = ProtocolConfig::get_for_max_version_UNSAFE();
        protocol_config.set_consensus_gc_depth_for_testing(gc_depth);

        for (index, _authority_info) in committee.authorities() {
            let dir = TempDir::new().unwrap();
            let (authority, commit_receiver, block_receiver) = make_authority(
                index,
                &dir,
                committee.clone(),
                keypairs.clone(),
                NetworkType::Tonic,
                boot_counters[index],
                protocol_config.clone(),
            )
            .await;
            boot_counters[index] += 1;
            commit_receivers.push(commit_receiver);
            block_receivers.push(block_receiver);
            authorities.insert(index, authority);
            temp_dirs.insert(index, dir);
        }

        // Now we take the receiver of authority 1 and we wait until we see at least one block committed from this authority
        // We wait until we see at least one committed block authored from this authority. That way we'll be 100% sure that
        // at least one block has been proposed and successfully received by a quorum of nodes.
        let index_1 = committee.to_authority_index(1).unwrap();
        'outer: while let Some(result) =
            timeout(Duration::from_secs(10), commit_receivers[index_1].recv())
                .await
                .expect("Timed out while waiting for at least one committed block from authority 1")
        {
            for block in result.blocks {
                if block.round() > GENESIS_ROUND && block.author() == index_1 {
                    break 'outer;
                }
            }
        }

        // Stop authority 1 & 2.
        // * Authority 1 will be used to wipe out their DB and practically "force" the amnesia recovery.
        // * Authority 2 is stopped in order to simulate less than f+1 availability which will
        // make authority 1 retry during amnesia recovery until it has finally managed to successfully get back f+1 responses.
        // once authority 2 is up and running again.
        authorities.remove(&index_1).unwrap().stop().await;
        let index_2 = committee.to_authority_index(2).unwrap();
        authorities.remove(&index_2).unwrap().stop().await;
        sleep(Duration::from_millis(500)).await;
        sleep(Duration::from_secs(5)).await;

        // Authority 1: create a new directory to simulate amnesia. The node will start having participated previously
        // to consensus but now will attempt to synchronize the last own block and recover from there. It won't be able
        // to do that successfully as authority 2 is still down.
        let dir = TempDir::new().unwrap();
        // We do reset the boot counter for this one to simulate a "binary" restart
        boot_counters[index_1] = 0;
        let (authority, mut commit_receiver, _block_receiver) = make_authority(
            index_1,
            &dir,
            committee.clone(),
            keypairs.clone(),
            NetworkType::Tonic,
            boot_counters[index_1],
            protocol_config.clone(),
        )
        .await;
        boot_counters[index_1] += 1;
        authorities.insert(index_1, authority);
        temp_dirs.insert(index_1, dir);
        sleep(Duration::from_secs(5)).await;

        // Now spin up authority 2 using its earlier directly - so no amnesia recovery should be forced here.
        // Authority 1 should be able to recover from amnesia successfully.
        let (authority, _commit_receiver, _block_receiver) = make_authority(
            index_2,
            &temp_dirs[&index_2],
            committee.clone(),
            keypairs,
            NetworkType::Tonic,
            boot_counters[index_2],
            protocol_config.clone(),
        )
        .await;
        boot_counters[index_2] += 1;
        authorities.insert(index_2, authority);
        sleep(Duration::from_secs(5)).await;

        // We wait until we see at least one committed block authored from this authority
        'outer: while let Some(result) = commit_receiver.recv().await {
            for block in result.blocks {
                if block.round() > GENESIS_ROUND && block.author() == index_1 {
                    break 'outer;
                }
            }
        }

        // Stop all authorities and exit.
        for (_, authority) in authorities {
            authority.stop().await;
        }
    }

    // TODO: create a fixture
    async fn make_authority(
        index: AuthorityIndex,
        db_dir: &TempDir,
        committee: Committee,
        keypairs: Vec<(NetworkKeyPair, ProtocolKeyPair)>,
        network_type: NetworkType,
        boot_counter: u64,
        protocol_config: ProtocolConfig,
    ) -> (
        ConsensusAuthority,
        UnboundedReceiver<CommittedSubDag>,
        UnboundedReceiver<CertifiedBlocksOutput>,
    ) {
        let registry = Registry::new();

        // Cache less blocks to exercise commit sync.
        let parameters = Parameters {
            db_path: db_dir.path().to_path_buf(),
            dag_state_cached_rounds: 5,
            commit_sync_parallel_fetches: 2,
            commit_sync_batch_size: 3,
            sync_last_known_own_block_timeout: Duration::from_millis(2_000),
            ..Default::default()
        };
        let txn_verifier = NoopTransactionVerifier {};

        let protocol_keypair = keypairs[index].1.clone();
        let network_keypair = keypairs[index].0.clone();

        let (commit_consumer, commit_receiver, block_receiver) = CommitConsumerArgs::new(0, 0, [0; 32], 0);

        let authority = ConsensusAuthority::start(
            network_type,
            0,
            0,
            0,
            index,
            committee,
            parameters,
            protocol_config,
            protocol_keypair,
            network_keypair,
            Arc::new(Clock::default()),
            Arc::new(txn_verifier),
            commit_consumer,
            registry,
            boot_counter,
            None,
            None, // legacy_store_manager
        )
        .await;

        (authority, commit_receiver, block_receiver)
    }

    /*
    /// Get network client for sending epoch change messages
    pub fn network_client(&self) {
        match self {
            ConsensusAuthority::WithTonic(node) => {
                let client = node.network_manager.client();
                // TODO: return client for epoch change use
            }
        }
    }
    */
