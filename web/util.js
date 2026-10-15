// Helpers shared by the UI modules. Every value interpolated into innerHTML
// goes through esc().
export const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export const badge = (text, tone='') => `<span class="badge ${esc(tone)}">${esc(text)}</span>`;
export const reviewText = s => s==='reviewed'?'Reviewed':s==='needs_review'?'Needs review':'Not reviewed';
export const reviewBadge = s => badge(reviewText(s), s==='reviewed'?'gray':'amber');
export const text = v => typeof v === 'string' ? v : '';
export const array = v => Array.isArray(v) ? v : [];
export const objects = v => array(v).filter(x => x && typeof x === 'object' && !Array.isArray(x));
