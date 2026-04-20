export interface Notification {
  id: string;
  createdAt: number;
  message: string;
}

export interface NotificationResponse {
  notifications: Array<{
    id: string;
    createdAt: number;
    message: string;
  }>;
  total: number;
  page: number;
  pageSize: number;
  totalPages: number;
}
