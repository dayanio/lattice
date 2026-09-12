import request from '@/api/request';



export const listPolicy = (data:any) => request.get('/policies/list', data);
export const createPolicy = (data:any) => request.post('/policies/create', data);
export const updatePolicy = (data:any) => request.put('/policies/create', data);
export const deletePolicy = (id:any) => request.delete(`/policies/${id}`);
// 描述即策略：自然语言翻译 + 确定性效果预览
export const translatePolicy = (data: { description: string }) => request.post('/policies/translate', data);
export const previewPolicy = (data: any) => request.post('/policies/preview', data);
export const policyDeliveryStatus = () => request.get('/policies/status');