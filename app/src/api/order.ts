function getAuthToken(): string {
  try {
    const token = localStorage.getItem('boko_auth_token') || '';
    if (!token || token.startsWith('mock-token-')) {
      return '';
    }
    return token;
  } catch {
    return '';
  }
}

export interface CreateCodOrderItem {
  book_id?: number;
  title: string;
  price: number;
  quantity: number;
}

export interface CreateCodOrderParams {
  amount: number;
  shippingAddress: string;
  phone: string;
  email: string;
  customerName?: string;
  couponCode?: string;
  idempotencyKey?: string;
  items: CreateCodOrderItem[];
}

export interface CodOrderResponse {
  message: string;
  order_id: number;
  total: number;
  status: string;
  payment_method: string;
  payment_status: string;
  order?: any;
}

export interface OrderQuoteResponse {
  subtotal: number;
  discount_percent: number;
  discount_amount: number;
  total: number;
  coupon_code?: string;
}

/**
 * Lấy báo giá chuẩn từ Backend trước khi submit đơn hàng
 * Endpoint: POST /api/orders/quote
 */
export async function getOrderQuoteApi(params: {
  couponCode?: string;
  items: { book_id: number; quantity: number }[];
}): Promise<OrderQuoteResponse> {
  const baseUrl = getBackendBaseUrl();
  const response = await fetch(`${baseUrl}/api/orders/quote`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      ...authHeaders(),
    },
    body: JSON.stringify({
      coupon_code: params.couponCode || '',
      items: params.items,
    }),
  });

  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || 'Lỗi tính báo giá đơn hàng');
  }
  return data;
}

export interface ConfirmReceiptResponse {
  message: string;
  order: any;
  status: string;
  payment_status: string;
}

function getBackendBaseUrl(): string {
  if (typeof window !== 'undefined') {
    const fromMeta = (import.meta as { env?: Record<string, string> }).env?.VITE_BACKEND_URL;
    if (fromMeta && typeof fromMeta === 'string' && fromMeta.trim() !== '') {
      return fromMeta.replace(/\/+$/, '');
    }
  }
  //return 'http://localhost:8080';
  return '/api';
}

function authHeaders(): Record<string, string> {
  const token = getAuthToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/**
 * Tạo đơn hàng COD trực tiếp từ checkout
 * Endpoint: POST /api/orders/cod/create
 */
export async function createCodOrderApi(params: CreateCodOrderParams): Promise<CodOrderResponse> {
  const baseUrl = getBackendBaseUrl();
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
    ...authHeaders(),
  };
  if (params.idempotencyKey) {
    headers['X-Idempotency-Key'] = params.idempotencyKey;
  }

  const response = await fetch(`${baseUrl}/api/orders/cod/create`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      amount: params.amount,
      shipping_address: params.shippingAddress,
      phone: params.phone,
      email: params.email,
      customer_name: params.customerName || '',
      coupon_code: params.couponCode || '',
      items: params.items,
    }),
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi tạo đơn hàng COD: HTTP ${response.status}`);
  }

  return data;
}

/**
 * Người mua xác nhận đã nhận hàng COD
 * Endpoint: POST /api/orders/:id/confirm-receipt
 */
export async function confirmOrderReceivedApi(orderId: number | string): Promise<ConfirmReceiptResponse> {
  const baseUrl = getBackendBaseUrl();
  const response = await fetch(`${baseUrl}/api/orders/${orderId}/confirm-receipt`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      ...authHeaders(),
    },
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi xác nhận nhận hàng: HTTP ${response.status}`);
  }

  return data;
}

/**
 * Lấy danh sách lịch sử đơn hàng của tôi
 * Endpoint: GET /api/orders/my-orders
 */
export async function getMyOrdersApi(): Promise<any[]> {
  const baseUrl = getBackendBaseUrl();
  const url = `${baseUrl}/api/orders/my-orders`;

  const response = await fetch(url, {
    method: 'GET',
    headers: {
      Accept: 'application/json',
      ...authHeaders(),
    },
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi lấy danh sách đơn hàng: HTTP ${response.status}`);
  }

  return data.data || [];
}
