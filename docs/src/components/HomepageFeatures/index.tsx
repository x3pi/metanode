import type {ReactNode} from 'react';
import clsx from 'clsx';
import Heading from '@theme/Heading';
import styles from './styles.module.css';

type FeatureItem = {
  title: string;
  icon: string;
  description: ReactNode;
};

const FeatureList: FeatureItem[] = [
  {
    title: 'Rust BFT Consensus',
    icon: '🦀',
    description: (
      <>
        Bộ máy đồng thuận BFT hiệu năng cao bằng Rust. Sử dụng cấu trúc DAG và cơ chế <strong>Zero-Timeout</strong> giúp hệ thống luôn tiến triển trơn tru, loại bỏ hoàn toàn rủi ro phân nhánh (Fork).
      </>
    ),
  },
  {
    title: 'EVM Execution Layer',
    icon: '⚡',
    description: (
      <>
        Lớp thực thi tương thích EVM tối ưu bằng Go. Tận dụng mô hình <strong>Actor Model</strong> để xử lý giao dịch song song tránh race condition, kết hợp parallel state pipeline để đạt TPS vượt trội.
      </>
    ),
  },
  {
    title: 'Zero-Fork Safeguards',
    icon: '🛡️',
    description: (
      <>
        Hệ thống an toàn tuyệt đối dựa trên <strong>Peer Attestation</strong> và Quorum Verification (2f+1). Tự động phục hồi trạng thái P2P và chuyển giao Epoch mượt mà.
      </>
    ),
  },
];

function Feature({title, icon, description}: FeatureItem) {
  return (
    <div className={clsx('col col--4')}>
      <div className="text--center" style={{ fontSize: '5rem', marginBottom: '1.5rem' }}>
        <span role="img" aria-label={title}>{icon}</span>
      </div>
      <div className="text--center padding-horiz--md">
        <Heading as="h3">{title}</Heading>
        <p>{description}</p>
      </div>
    </div>
  );
}

export default function HomepageFeatures(): ReactNode {
  return (
    <section className={styles.features}>
      <div className="container">
        <div className="row">
          {FeatureList.map((props, idx) => (
            <Feature key={idx} {...props} />
          ))}
        </div>
      </div>
    </section>
  );
}
