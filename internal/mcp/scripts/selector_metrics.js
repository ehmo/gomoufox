({selector}) => {
  const element = document.querySelector(selector);
  if (!element) return {width: 0, height: 0};
  const rect = element.getBoundingClientRect();
  const width = Math.ceil(Math.max(rect.width || 0, element.scrollWidth || 0, element.offsetWidth || 0));
  const height = Math.ceil(Math.max(rect.height || 0, element.scrollHeight || 0, element.offsetHeight || 0));
  return {width, height};
}
