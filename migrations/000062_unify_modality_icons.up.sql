-- 同一模态的输入/输出共用同一个图标（产品决策：方向由 key/语境表达，图标只表达模态，允许重复）。
-- text/image 直接复用 .input 已配置的图标；audio 两侧统一为中性波形（audio-lines），
-- 不再用「麦克风=输入 / 喇叭=输出」两套。

UPDATE public.capability_keys
SET icon_svg = (SELECT icon_svg FROM public.capability_keys WHERE key = 'text.input')
WHERE key = 'text.output';

UPDATE public.capability_keys
SET icon_svg = (SELECT icon_svg FROM public.capability_keys WHERE key = 'image.input')
WHERE key = 'image.output';

UPDATE public.capability_keys
SET icon_svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 10v3"/><path d="M6 6v11"/><path d="M10 3v18"/><path d="M14 8v7"/><path d="M18 5v13"/><path d="M22 10v3"/></svg>'
WHERE key IN ('audio.input', 'audio.output');
