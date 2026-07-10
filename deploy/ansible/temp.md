all:
  children:
    metanode_cluster:
      vars:
        ansible_user: "abc"
      hosts:
        192.168.1.234_sync:
          node_ids: [4]
          synconly_nodes: [4]
          rpc_nodes: [4]
          ansible_connection: local
          ansible_host: 192.168.1.234
        192.168.1.234_validator:
          node_ids: [0]
          ansible_connection: local
          ansible_host: 192.168.1.234
        192.168.1.233_validator:
          node_ids: [1]
          ansible_host: 192.168.1.233
        192.168.1.230_validator:
          node_ids: [2,3]
          ansible_host: 192.168.1.230






all:
  children:
    metanode_cluster:
      vars:
        ansible_user: "abc"
      hosts:
        192.168.1.234_sync:
          node_ids: [4]
          synconly_nodes: [4]
          rpc_nodes: [4]
          ansible_connection: local
          ansible_host: 192.168.1.234
        192.168.1.234_validator:
          node_ids: [0,1,2,3]
          ansible_connection: local
          ansible_host: 192.168.1.234
