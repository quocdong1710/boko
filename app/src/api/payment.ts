import { getBackendBaseUrl } from './serverAuth';

export interface MomoPaymentResponse {
  message: string;
  order_id: number;
  amount: number;
  momo_order_id: string;
  pay_url: string;
  deeplink: string;
  qr_code_url: string;
  applink: string;
}

export interface PaymentStatusResponse {
  order_id: number;
  total: number;
  status: string;
  payment_method: string;
  payment_status: string;
  payment_trans_id: string;
  payment_order_id: string;
  updated_at: string;
}

export interface CreateMomoPaymentParams {
  orderId?: number;
  amount?: number;
  shippingAddress?: string;
  phone?: string;
  email?: string;
  redirectUrl?: string;
  requestType?: string;
}

function getAuthToken(): string {
  try {
    const token = localStorage.getItem('boko_auth_token') || '';
    // Nếu là mock-token từ chế độ giả lập offline thì không gửi lên backend thật
    if (!token || token.startsWith('mock-token-')) {
      return '';
    }
    return token;
  } catch {
    return '';
  }
}

function getBaseUrl(): string {
  // Trả về VITE_BACKEND_URL nếu được set (local dev),
  // hoặc '' để dùng relative path /api/... qua nginx proxy (Docker deploy)
  return getBackendBaseUrl();
}

function authHeaders(): Record<string, string> {
  const token = getAuthToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/**
 * Khởi tạo yêu cầu thanh toán MoMo Sandbox qua Backend Go
 * Endpoint: POST /api/payment/momo/create
 */
export async function createMomoPaymentApi(
  params: CreateMomoPaymentParams
): Promise<MomoPaymentResponse> {
  const baseUrl = getBaseUrl();
  const token = getAuthToken();

  const response = await fetch(`${baseUrl}/api/payment/momo/create`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({
      order_id: params.orderId || 0,
      amount: params.amount,
      shipping_address: params.shippingAddress,
      phone: params.phone,
      email: params.email,
      redirect_url: params.redirectUrl || `${window.location.origin}/payment/momo-callback`,
      request_type: params.requestType || 'payWithATM',
    }),
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    // Nếu token bị hết hạn hoặc không hợp lệ, tự động dọn dẹp để không làm kẹt các lần thanh toán tiếp theo
    if (response.status === 401) {
      try {
        localStorage.removeItem('boko_auth_token');
      } catch { }
    }
    throw new Error(data.error || data.message || `Lỗi khi kết nối MoMo: HTTP ${response.status}`);
  }

  return data as MomoPaymentResponse;
}

/**
 * Tra cứu trạng thái thanh toán của đơn hàng (có tự động đồng bộ từ MoMo Gateway)
 * Endpoint: GET /api/payment/momo/status/:id
 */
export async function getPaymentStatusApi(orderId: number | string): Promise<PaymentStatusResponse> {
  const baseUrl = getBaseUrl();
  const token = getAuthToken();

  const response = await fetch(`${baseUrl}/api/payment/momo/status/${orderId}`, {
    method: 'GET',
    headers: {
      Accept: 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi tra cứu đơn hàng: HTTP ${response.status}`);
  }

  return data as PaymentStatusResponse;
}

/**
 * Giả lập kích hoạt Webhook MoMo IPN thành công (dành cho kiểm thử nội bộ)
 * Endpoint: POST /api/payment/momo/mock-ipn/:id
 */
export async function mockMomoIpnApi(orderId: number | string): Promise<{ message: string; payment_status: string }> {
  const baseUrl = getBaseUrl();

  const response = await fetch(`${baseUrl}/api/payment/momo/mock-ipn/${orderId}`, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      ...authHeaders(),
    },
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi mock IPN: HTTP ${response.status}`);
  }

  return data;
}

export interface MomoSimulatorParams {
  orderId: string;
  resultCode: number;
  payType?: string;
  tamperSignature?: boolean;
}

export interface MomoSimulatorResponse {
  success: boolean;
  security_status: string;
  message: string;
  order_id: number;
  payment_order_id: string;
  trans_id: string;
  amount: number;
  result_code: number;
  raw_signature_data?: string;
  signature?: string;
  redirect_url?: string;
  error?: string;
  submitted_signature?: string;
  expected_signature?: string;
  tampered?: boolean;
}

/**
 * Gửi yêu cầu thanh toán qua Cổng Giả Lập MoMo Sandbox
 * Endpoint: POST /api/payment/momo/simulator-ipn
 */
export async function submitMomoSimulatorApi(
  params: MomoSimulatorParams
): Promise<MomoSimulatorResponse> {
  const baseUrl = getBaseUrl();

  const response = await fetch(`${baseUrl}/api/payment/momo/simulator-ipn`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      ...authHeaders(),
    },
    body: JSON.stringify({
      order_id: params.orderId,
      result_code: params.resultCode,
      pay_type: params.payType || 'credit',
      tamper_signature: !!params.tamperSignature,
    }),
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    return {
      success: false,
      security_status: data.security_status || 'ERROR',
      message: data.error || data.message || `Lỗi: HTTP ${response.status}`,
      order_id: 0,
      payment_order_id: params.orderId,
      trans_id: '',
      amount: 0,
      result_code: params.resultCode,
      raw_signature_data: data.raw_signature_data,
      submitted_signature: data.submitted_signature,
      expected_signature: data.expected_signature,
      tampered: data.tampered,
    };
  }

  return data as MomoSimulatorResponse;
}

// ==================== VNPAY API ====================

export interface CreateVnpayPaymentParams {
  orderId?: number;
  amount: number;
  shippingAddress?: string;
  phone?: string;
  email?: string;
  bankCode?: string;
  paymentMethod?: string;
  redirectUrl?: string;
}

export interface VnpayPaymentResponse {
  message: string;
  order_id: number;
  amount: number;
  payment_url: string;
  txn_ref: string;
}

/**
 * Khởi tạo yêu cầu thanh toán VNPAY Sandbox
 * Endpoint: POST /api/payment/vnpay/create
 */
export async function createVnpayPaymentApi(
  params: CreateVnpayPaymentParams
): Promise<VnpayPaymentResponse> {
  const baseUrl = getBaseUrl();
  const token = getAuthToken();

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
  };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const response = await fetch(`${baseUrl}/api/payment/vnpay/create`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      order_id: params.orderId || 0,
      amount: params.amount,
      shipping_address: params.shippingAddress || '',
      phone: params.phone || '',
      email: params.email || '',
      bank_code: params.bankCode || '',
      payment_method: params.paymentMethod || '',
      redirect_url: params.redirectUrl || '',
    }),
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi tạo thanh toán VNPAY: HTTP ${response.status}`);
  }

  return data;
}

/**
 * Lấy trạng thái đơn hàng thanh toán VNPAY từ Backend
 * Endpoint: GET /api/payment/vnpay/status/:id
 */
export async function getVnpayStatusApi(
  orderId: number | string,
  queryString?: string
): Promise<PaymentStatusResponse> {
  const baseUrl = getBaseUrl();
  const token = getAuthToken();

  const headers: Record<string, string> = {
    Accept: 'application/json',
  };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const query = queryString ? (queryString.startsWith('?') ? queryString : `?${queryString}`) : '';
  const response = await fetch(`${baseUrl}/api/payment/vnpay/status/${orderId}${query}`, {
    method: 'GET',
    headers,
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi lấy trạng thái VNPAY: HTTP ${response.status}`);
  }

  return data;
}

// ==================== PAYPAL API ====================

export interface CreatePaypalPaymentParams {
  orderId?: number;
  amount?: number;
  shippingAddress?: string;
  phone?: string;
  email?: string;
  redirectUrl?: string;
}

export interface PaypalPaymentResponse {
  message: string;
  order_id: number;
  amount: number;
  amount_usd: number;
  paypal_order_id: string;
  approve_url: string;
}

/**
 * Khởi tạo giao dịch PayPal cho đơn hàng (cần đăng nhập, đơn thuộc về user)
 * Endpoint: POST /api/payment/paypal/create
 */
export async function createPaypalPaymentApi(
  params: CreatePaypalPaymentParams
): Promise<PaypalPaymentResponse> {
  const baseUrl = getBackendBaseUrl();
  // if (!baseUrl) throw new Error('Chưa cấu hình backend (VITE_BACKEND_URL).');

  const response = await fetch(`${baseUrl}/api/payment/paypal/create`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      ...authHeaders(),
    },
    body: JSON.stringify({
      order_id: params.orderId || 0,
      amount: params.amount,
      shipping_address: params.shippingAddress || '',
      phone: params.phone || '',
      email: params.email || '',
      redirect_url: params.redirectUrl || `${window.location.origin}/payment/paypal-callback`,
    }),
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi tạo thanh toán PayPal: HTTP ${response.status}`);
  }

  return data;
}

/**
 * Thu tiền đơn PayPal đã được user approve
 * Endpoint: POST /api/payment/paypal/capture
 */
export async function capturePaypalPaymentApi(
  params: { paypalOrderId: string }
): Promise<PaymentStatusResponse & { message: string }> {
  const baseUrl = getBackendBaseUrl();
  // if (!baseUrl) throw new Error('Chưa cấu hình backend (VITE_BACKEND_URL).');

  const response = await fetch(`${baseUrl}/api/payment/paypal/capture`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      ...authHeaders(),
    },
    body: JSON.stringify({ paypal_order_id: params.paypalOrderId }),
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi capture PayPal: HTTP ${response.status}`);
  }

  return data;
}

/**
 * Lấy trạng thái đơn hàng thanh toán PayPal từ Backend
 * Endpoint: GET /api/payment/paypal/status/:id
 */
export async function getPaypalStatusApi(
  orderId: number | string
): Promise<PaymentStatusResponse> {
  const baseUrl = getBackendBaseUrl();
  // if (!baseUrl) throw new Error('Chưa cấu hình backend (VITE_BACKEND_URL).');

  const response = await fetch(`${baseUrl}/api/payment/paypal/status/${orderId}`, {
    headers: {
      Accept: 'application/json',
      ...authHeaders(),
    },
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Lỗi lấy trạng thái PayPal: HTTP ${response.status}`);
  }

  return data;
}

// ==================== NCB ATM DIRECT OTP API ====================

export interface VerifyAtmOtpParams {
  amount: number;
  cardNumber?: string;
  cardHolder?: string;
  bankName?: string;
  otp: string;
  shippingAddress?: string;
  phone?: string;
  email?: string;
  customerName?: string;
  couponCode?: string;
  items?: Array<{
    book_id: number;
    title: string;
    price: number;
    quantity: number;
  }>;
}

export interface VerifyAtmOtpResponse {
  success: boolean;
  message: string;
  order_id: number;
  transaction_id: string;
  total: number;
  payment_status: string;
  status: string;
}

/**
 * Xác thực mã OTP Thẻ ATM Nội Địa NCB 1-chạm
 * Endpoint: POST /api/payment/atm/verify-otp
 */
export async function verifyAtmOtpPaymentApi(
  params: VerifyAtmOtpParams
): Promise<VerifyAtmOtpResponse> {
  const baseUrl = getBackendBaseUrl();
  // if (!baseUrl) throw new Error('Chưa cấu hình backend (VITE_BACKEND_URL).');

  const response = await fetch(`${baseUrl}/api/payment/atm/verify-otp`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
      ...authHeaders(),
    },
    body: JSON.stringify({
      amount: params.amount,
      card_number: params.cardNumber || '',
      card_holder: params.cardHolder || '',
      bank_name: params.bankName || '',
      otp: params.otp,
      shipping_address: params.shippingAddress || '',
      phone: params.phone || '',
      email: params.email || '', 
      customer_name: params.customerName || '',
      coupon_code: params.couponCode || '',
      items: params.items || [],
    }),
  });

  const data = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(data.error || data.message || `Xác thực OTP thất bại: HTTP ${response.status}`);
  }

  return data as VerifyAtmOtpResponse;
}
