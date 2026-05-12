use std::cmp::Ordering;

fn main() {
    let mut keys = vec![
        "hK+5uAd/qeLvGo98NE7OCFwYioigpG0uYXn5ocETYSrUMLbCkC".to_string(), // Node-2
        "iRQSDQnNqf0A0RkLh33pIN1p10gP1vE+i7Q9kL9xZ1LdG3sO".to_string(), // Node-1
        "kUYrYvfHXXN6+q2L9T6Zz94w5cM1a49+j/n7T0zXU++2F+G4".to_string(), // Node-0
        "lexRcM9Z5J+M7G+0z8z30x4+G8fQz96O4Z4q+PzL+8tD".to_string(), // Node-3
    ];
    
    keys.sort();
    
    for k in &keys {
        println!("{}", k);
    }
}
