use std::cmp::Ordering;
use std::collections::BTreeSet;

fn main() {
    let mut round = 1;

    let author_0 = vec![1, 2, 3, 12000];
    let author_1 = vec![1, 2, 3, 12000];
    let author_2 = vec![1, 2, 3, 12000];
    let author_3 = vec![1, 2, 3, 12000];
    
    let committee_size = 4;
    let threshold = 3;

    let mut aggregator = BTreeSet::new();

    let process_block = |block_round: u32, author: usize, round: &mut u32, aggregator: &mut BTreeSet<usize>| {
        match block_round.cmp(round) {
            Ordering::Less => false,
            Ordering::Equal => {
                if aggregator.insert(author) {
                    if aggregator.len() >= threshold {
                        aggregator.clear();
                        *round = block_round + 1;
                        return true;
                    }
                }
                false
            }
            Ordering::Greater => {
                aggregator.clear();
                aggregator.insert(author);
                *round = block_round;
                false
            }
        }
    };

    println!("Initial threshold round: {}", round);

    for (author, blocks) in [author_0, author_1, author_2, author_3].iter().enumerate() {
        for block_round in blocks {
            process_block(*block_round, author, &mut round, &mut aggregator);
            println!("Processed Author {} Round {}. Threshold round is now: {}", author, block_round, round);
        }
    }

    println!("Final threshold round: {}", round);
}
