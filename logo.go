package main

import "fyne.io/fyne/v2"

// Low-poly origami elephant mark in the TablePlus orange family.
const logoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 104">
<g>
  <polygon points="30,24 66,12 56,44" fill="#F8C860"/>
  <polygon points="66,12 84,34 56,44" fill="#F5A623"/>
  <polygon points="84,34 80,58 56,44" fill="#E8862E"/>
  <polygon points="80,58 56,72 56,44" fill="#D9731A"/>
  <polygon points="30,24 56,44 36,66" fill="#F5A623"/>
  <polygon points="36,66 56,44 56,72" fill="#E8862E"/>
  <polygon points="30,24 36,66 24,46" fill="#EFA43C"/>
  <polygon points="22,26 40,16 36,52" fill="#D9731A"/>
  <polygon points="22,26 26,50 36,52" fill="#C46311"/>
  <polygon points="56,72 68,74 70,90" fill="#F5A623"/>
  <polygon points="56,72 70,90 60,94" fill="#E8862E"/>
  <polygon points="70,90 60,94 62,84" fill="#D9731A"/>
  <polygon points="48,70 55,73 50,84" fill="#FBEBC8"/>
  <polygon points="34,68 40,72 36,80" fill="#F5A623"/>
  <polygon points="42,68 47,72 43,79" fill="#E8862E"/>
  <circle cx="42" cy="34" r="3.4" fill="#4A2E10"/>
  <circle cx="43.2" cy="32.8" r="1.1" fill="#FFFFFF"/>
</g>
</svg>`

var logoResource = fyne.NewStaticResource("elephant-logo.svg", []byte(logoSVG))
