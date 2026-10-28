// bodycam.cpp : implementation file
//

#include "stdafx.h"
#include "common.h"
#include "chat.h"
#include "bbox.h"
#include "pe.h"
#include "dib.h"
#include "avatar.h"
#include "bodycam.h"

#include "saywnd.h"
#include "chatprot.h"
#include "ui.h"
#include "vector2d.h"
#include <math.h>
#include "resource.h"
#include "binddoc.h"
#include "chatdoc.h"


#ifdef _DEBUG
#undef THIS_FILE
static char BASED_CODE THIS_FILE[] = __FILE__;
#endif

// Define a nice background color for the image which will replace the ugly color used for transparency in the world.
#define iBackgroundRed	 128
#define iBackgroundGreen 128
#define iBackgroundBlue	 128

extern CChatApp theApp;

/////////////////////////////////////////////////////////////////////////////
// CBodyCam

static CString emotionName[9];

void LoadEmotionStrings() {
	int startID = ID_EM_HAPPY; // assumption: happy is first, contiguous ids
	for (int i = 0; i < 9; i++)
		emotionName[i].LoadString(startID++);
}

char *sm_icons[] =
{
	"fc_hp2_s.bmp",
	"fc_coy_s.bmp",
	"fc_bor_s.bmp",
	"fc_sca_s.bmp",
	"fc_sad_s.bmp",
	"fc_ang_s.bmp",
	"fc_sho_s.bmp",
	"fc_laf_s.bmp",
};
char *lg_icons[] =
{
	"fc_hap_l.bmp",
	"fc_coy_l.bmp",
	"fc_bor_l.bmp",
	"fc_sca_l.bmp",
	"fc_sad_l.bmp",
	"fc_ang_l.bmp",
	"fc_sho_l.bmp",
	"fc_laf_l.bmp",
};

#define LG_ICON_WIDTH	20
#define LG_ICON_HEIGHT	26
#define SM_ICON_WIDTH	13
#define SM_ICON_HEIGHT	18

IMPLEMENT_DYNCREATE(CBodyCam, CWnd)

CBodyCam::CBodyCam()
{
	m_avatar = NULL;
	m_palette = NULL;
	m_mouseDown = FALSE;
	m_forcedDelete = TRUE;		// indicates bodycam must be deleted in OnNCDestroy
	m_talkToHeight = TALKTOHEIGHT;
	m_emotion.Set(0.0, 0.0);	// start off neutral
	m_retDib = NULL;			// allocated later by CreateRetainedPanel
	m_retSec = NULL;
	m_lastEmotionString = NULL;
//	strcpy(m_toolTipString, "HOWDY!!!");					// ToolTip support
	VERIFY(m_toolTip.Create(this, TTS_ALWAYSTIP));
	VERIFY(m_toolTip.AddTool(this, m_toolTipString));
	EnableToolTips(TRUE);
	m_toolTip.Activate(TRUE);

	char buff[80];
	for (int i = 0; i < NEMOTIONS; i++) {
		sprintf(buff, "%s\\%s", theApp.GetAvatarDir(), lg_icons[i]);
		VERIFY(m_icons[i].Load(buff));
	}
}

CBodyCam::~CBodyCam()
{
	FreeRetainedPanel();
	if (m_palette) delete m_palette;
	// don't need to explicitly free icons since they aren't pointers
}


BEGIN_MESSAGE_MAP(CBodyCam, CWnd)
	//{{AFX_MSG_MAP(CBodyCam)
	ON_WM_PAINT()
	ON_WM_LBUTTONDOWN()
	ON_WM_MOUSEMOVE()
	ON_WM_LBUTTONUP()
	ON_WM_CHAR()
	ON_WM_CONTEXTMENU()
	ON_COMMAND(ID_BODYCONTEXT_FREEZE, OnBodycontextFreeze)
	ON_COMMAND(ID_BODYCONTEXT_SENDEXPRESSION, OnBodycontextSendexpression)
	ON_WM_SIZE()
	ON_WM_NCDESTROY()
	ON_UPDATE_COMMAND_UI(ID_BODYCONTEXT_FREEZE, OnUpdateBodycontextFreeze)
	ON_WM_KILLFOCUS()
	//}}AFX_MSG_MAP
END_MESSAGE_MAP()


/////////////////////////////////////////////////////////////////////////////
// CBodyCam message handlers

BOOL CBodyCam::Create(LPCTSTR lpszClassName, LPCTSTR lpszWindowName, DWORD dwStyle, const RECT& rect, CWnd* pParentWnd, UINT nID, CCreateContext* pContext) 
{
	dwStyle &= ~WS_BORDER;

	return CWnd::Create(lpszClassName, lpszWindowName, dwStyle, rect, pParentWnd, nID, pContext);
}


void CBodyCam::OnPaint() 
{
	CPaintDC dc(this); // device context for painting
	CRect rect;
    CPen *ppenOld;
	CBrush *pbrOld;
	CPalette *oldPal;

//	double randfloat();
//	dc.FillSolidRect(0, 0, 1000, 1000, RGB((int)(randfloat()*255), (int)(randfloat()*255), (int)(randfloat()*255)));

	// Use our own palette
	if (oldPal = dc.SelectPalette(GetPalette(&dc), FALSE))
        dc.RealizePalette();

	// figure out rectangle
	GetClientRect(&rect);

	// get background brush
	pbrOld = (CBrush *) dc.SelectStockObject(WHITE_BRUSH);
    ppenOld = (CPen *) dc.SelectStockObject(BLACK_PEN);

	DrawBullsEye(&dc, rect);
	if (!m_bullDisabled) {
		DrawBullsEyeCons(&dc, rect);

		m_cursorPos = GetPointFromEmotion(m_emotion);	// calculate point from emotion in case there was a resize
		DrawCursor(m_cursorPos, &dc);
	}

	// need to clear part of window north of emotion wheel (why isn't this done automatically?
	dc.FillSolidRect(0, 0, rect.right, rect.bottom - (rect.right - rect.left), RGB(255, 255, 255));

	// now draw body feedback
	if (m_avatar) {
		CBody *body = m_avatar->m_body;
		if (body) m_bodyRect = DrawBody(&dc, body);
		DrawTalkTos(m_avatar, &rect);
	}

	// restore
	if (oldPal)
		dc.SelectPalette(oldPal, FALSE);
	dc.SelectObject( pbrOld );
    dc.SelectObject( ppenOld );
}

#define MAXBULL	159
#define MINBULL	93

// sets m_bullSide and m_bullDisabled
void CBodyCam::CacheBullSide(int width) inline {
	m_bullSide = min(width, MAXBULL);
	if (m_bullSide < MINBULL) {
		m_bullDisabled = TRUE;
		m_bullSide = MINBULL;
	} else m_bullDisabled = FALSE;
}

void CBodyCam::DrawBullsEye(CDC *dc, RECT &rect) {
	int width = rect.right - rect.left;
	int halfSide = m_bullSide / 2;

	m_bullsEye.x = (rect.left + rect.right) / 2;
	m_bullsEye.y = rect.bottom - halfSide;
	m_bullRadius = halfSide - m_cursorRadius - m_iconHeight;

	// fill widget background w/ gray
	dc->FillSolidRect(rect.left, rect.bottom - m_bullSide, width, m_bullSide, PALETTERGB(210, 210, 210));
	if (m_bullDisabled) return;

	CRect circRect(m_bullsEye.x - m_bullRadius, m_bullsEye.y - m_bullRadius,
				   m_bullsEye.x + m_bullRadius, m_bullsEye.y + m_bullRadius);
	dc->Ellipse(circRect);
	DrawPoint(dc, m_bullsEye, 5);
	m_bullRadius -= m_cursorRadius;	// we do not want the cursor to leave the circle
}

RECT CBodyCam::GetIconRect(int i) const {
	RECT iconRect;
	int offsetFromEye = m_bullRadius + 2*m_cursorRadius + m_iconHeight/2;

	double angle = 2*PI*i / NEMOTIONS;
	POINT iconCenter = dpoint_to_point(point_scalmult(offsetFromEye, angle_to_vector(angle)));
	iconCenter = point_add(iconCenter, m_bullsEye);
	iconRect.top = iconCenter.y + m_iconHeight/2;
	iconRect.bottom = iconCenter.y - m_iconHeight/2;
	iconRect.left = iconCenter.x - m_iconWidth/2;
	iconRect.right = iconCenter.x + m_iconWidth/2;
	return iconRect;
}

void CBodyCam::DrawBullsEyeCons(CDC *dc, RECT &rect) {
	for (int i = 0; i < NEMOTIONS; i++) {
		RECT iconRect = GetIconRect(i);
		m_icons[i].Draw(dc, iconRect.left, iconRect.bottom); // iconRect is flipped to support hit-testing
	}
}


int CBodyCam::OnToolHitTest( CPoint point, TOOLINFO* pTI ) const {

	// CWnd::OnToolHitTest expects the tool window to be a child of the window to
	//   which the ToolTipCtrl is attached.  Since that's not the case here, we'll
	//   override the OnToolHitTest method to return a TOOLINFO referring to the
	//   CClientView.  -djk
	pTI->hwnd = m_hWnd;
	pTI->uId = (UINT)m_hWnd;
	pTI->uFlags |= (TTF_IDISHWND|TTF_NOTBUTTON);

	for (int i = 0; i < NEMOTIONS; i++) {
		RECT iconRect = GetIconRect(i);
		if (inside_bbox(&point, &iconRect)) {
			pTI->lpszText = strdup(emotionName[i]);
			return i;
		}
	}
	return -1;		// no hit
}

CString *StringFromEmotion(CEmotion &em) {
	if (em.m_intensity == 0.0) return &(emotionName[8]);
	if (em.m_emotion >= 7*PI/8 || em.m_emotion < -7*PI/8)
		return &(emotionName[4]);
	else if (em.m_emotion <= -5*PI/8) return &(emotionName[5]);
	else if (em.m_emotion <= -3*PI/8) return &(emotionName[6]);
	else if (em.m_emotion <= -PI/8) return &(emotionName[7]);
	else if (em.m_emotion > 5*PI/8) return &(emotionName[3]);
	else if (em.m_emotion > 3*PI/8) return &(emotionName[2]);
	else if (em.m_emotion > PI/8) return &(emotionName[1]);
	else return &(emotionName[0]);
}


void CBodyCam::OnLButtonDown(UINT nFlags, CPoint point) 
{
	// TODO: Add your message handler code here and/or call default
	
	CWnd::OnLButtonDown(nFlags, point);		// What does this do?
    SetFocus();
	if (m_bullDisabled) return;

	m_mouseDown = TRUE;
	SetCapture();		// continue to receive messages if mouse moves outside window
	CEmotion emotion = GetEmotionFromPoint(point);
	UpdateEmotion(emotion);

	if(m_avatar) // Make sure this didnt get NULLed for some reason
	{
		// Temporarily freeze bodycam
		if (m_avatar->m_freeze == AF_UNFROZEN) {
			m_avatar->m_freeze = AF_TEMPFROZEN;
	//		av->m_body->m_requested = TRUE;		// any way to get rid of two reps of this data?
		}
	}
}

void CBodyCam::OnMouseMove(UINT nFlags, CPoint point) 
{
	// TODO: Add your message handler code here and/or call default
	
	CWnd::OnMouseMove(nFlags, point);
	if (m_mouseDown) {
		CEmotion emotion = GetEmotionFromPoint(point);
		UpdateEmotion(emotion);
	}
}

void CBodyCam::OnLButtonUp(UINT nFlags, CPoint point) 
{
	// TODO: Add your message handler code here and/or call default
	
	CWnd::OnLButtonUp(nFlags, point);
	ASSERT(AfxGetApp());
	((CChatApp*)AfxGetApp())->ResetStatus();
	m_mouseDown = FALSE;
	ReleaseCapture();
}


void CBodyCam::DrawCursor(POINT &point, CDC *dc) {
	CRect cursor(point.x - m_cursorRadius, point.y + m_cursorRadius, point.x + m_cursorRadius, point.y - m_cursorRadius);
	CDC *newDC = NULL;
	if (!dc)
		newDC = dc = new CClientDC(this);

	int oldROP = dc->SetROP2(R2_XORPEN);
	dc->Ellipse(cursor);
	dc->SetROP2(oldROP);
	if (newDC) delete newDC;
}


CEmotion CBodyCam::GetEmotionFromPoint(POINT &point) {
	CEmotion emotion;

	POINT vec = point_sub(point, m_bullsEye);
	emotion.m_intensity = (float)point_magn(vec) / m_bullRadius;
	emotion.m_intensity = (float) min(emotion.m_intensity, 1.0);
	if (emotion.m_intensity < 0.2) emotion.m_intensity = (float)0.0;		// create a detente in the center
	emotion.m_emotion = (emotion.m_intensity == 0) ? 0 : (float) vector_to_angle(vec);

	return emotion;
}

POINT CBodyCam::GetPointFromEmotion(CEmotion &emotion) {
	DPOINT dvec = angle_to_vector(emotion.m_emotion);
	dvec = point_scalmult(m_bullRadius * emotion.m_intensity, dvec);
	POINT vec = dpoint_to_point(dvec);
	vec = point_add(m_bullsEye, vec);
	return vec;
}

void CBodyCam::UpdateEmotion(CEmotion &emotion) {
	POINT newPos = GetPointFromEmotion(emotion);
	if (newPos.x != m_cursorPos.x || newPos.y != m_cursorPos.y/* || !m_body*/) {  // !m_body forces a body change for initialization
		DrawCursor(m_cursorPos);		// erase old point
		DrawCursor(newPos);				// draw new point
		m_cursorPos = newPos;
		m_emotion = emotion;
		if (m_avatar) m_avatar->UpdateBody(m_avatar->GetBodyFromEmotion(emotion));
		if (m_mouseDown) {								// update status bar only if mousedown
			CString *em_str = StringFromEmotion(emotion);
			if (em_str != m_lastEmotionString) {		// update string only if necessary
				CString strEmotion;
				strEmotion.LoadString(ID_EMOTION_IS);
				VERIFY(ReplaceToken(strEmotion, CString("%1"), *em_str));
				ASSERT(AfxGetApp());
				((CChatApp*)AfxGetApp())->SetStatusPaneString(0,strEmotion);
				m_lastEmotionString = em_str;
			}
		}
	}
}



// Stub so that code not knowing details of bodycam can still set the emotion
void UpdateEmotion(CEmotion &emotion) {
	GetBodyCam()->UpdateEmotion(emotion);
}

void CBodyDouble::FlipBodyBox(RECT &fullRect, RECT &headRect, RECT &torsoRect) {
	int headWidth = headRect.right - headRect.left;
	int torsoWidth = torsoRect.right - torsoRect.left;

	headRect.left = fullRect.right - (headRect.left - fullRect.left);
	headRect.right = headRect.left - headWidth;
	
	torsoRect.left = fullRect.right - (torsoRect.left - fullRect.left);
	torsoRect.right = torsoRect.left - torsoWidth;
}

void CBodyCam::GetBodyRect(RECT &rect) inline {
	GetClientRect(&rect);
	rect.bottom -= m_bullSide;				   // subtract out bullseye
	rect.top += 2 * m_talkToHeight;
}

RECT CBodyCam::DrawBody(CDC *dc, CBody *body) {
	// calculate region for body draw
	RECT rect, rect2, brect;
	GetBodyRect(rect);

	if (rect.bottom >= rect.top) {		// if space shrinks to nothing or inverts, don't draw
		rect.bottom -= rect.top;
		int oldTop = rect.top;
		rect.top = 0;
		rect2 = rect;
		rect2.bottom -= rect2.top;
		rect2.top = 0;
		rect2.left -= 1000;		// so character remains a fixed height, clipped to width
		rect2.right += 1000;

		// set up offscreen bitmap
		CDC memDC;
		VERIFY(memDC.CreateCompatibleDC(dc));
		CBitmap temp;
		CBitmap *retCBit = temp.FromHandle(m_retSec);
		CBitmap *bmpOld = memDC.SelectObject(retCBit); // must use a CBitmap

		memDC.FillSolidRect(&m_bodyRect, RGB(255, 255, 255));
		brect = body->DrawBody(&memDC, rect2, FALSE);   // note: is the clippath set up right from compatible dc?
		VERIFY(dc->BitBlt(rect.left, oldTop, rect.right, rect.bottom, &memDC, 0, 0, SRCCOPY));
		
		memDC.SelectObject(bmpOld);		// cleanup
	} else {
		brect.left = brect.right = brect.top = brect.bottom = 0;
	}

	return brect;
}


RECT CBodyDouble::DrawBody(CDC *dc, RECT &clientRect, BOOL drawNimbus) {
	RECT fullRect, headRect, torsoRect;
	CPose *GetPoseFromID(unsigned short poseID, BOOL loadMask = TRUE);
	dc->SetStretchBltMode(STRETCH_HALFTONE); //COLORONCOLOR);
	CAvatarX *av = GetAvatar(m_avatarID);
	int flags = GetAvatar(m_avatarID)->m_flags;

	CPose *headPose = GetPoseFromID(m_faceRec->poseID, TRUE);
	CPose *torsoPose = GetPoseFromID(m_torsoRec->poseID, TRUE);
	GetBodyBox(headPose, torsoPose, clientRect, fullRect, headRect, torsoRect);
	if (m_flip) FlipBodyBox(fullRect, headRect, torsoRect);

	if (drawNimbus) {
		if (torsoPose->m_aura)
			torsoPose->m_aura->Draw(dc, torsoRect.left, torsoRect.top,
									torsoRect.right - torsoRect.left, torsoRect.bottom - torsoRect.top,
									MERGEPAINT);
		if (headPose->m_aura)
			headPose->m_aura->Draw(dc, headRect.left, headRect.top,
								   headRect.right - headRect.left, headRect.bottom - headRect.top,
								   MERGEPAINT);
	}

	if (flags & TORSOFIRST) {
		if ((flags & TORSOMASK) && torsoPose->m_mask)
			torsoPose->m_mask->Draw(dc, torsoRect.left, torsoRect.top,
									torsoRect.right - torsoRect.left, torsoRect.bottom - torsoRect.top,
									MERGEPAINT);
		torsoPose->m_drawing->Draw(dc, torsoRect.left, torsoRect.top,
								   torsoRect.right - torsoRect.left, torsoRect.bottom - torsoRect.top,
								   SRCAND);
	}

	if ((flags & HEADMASK) && headPose->m_mask)
		headPose->m_mask->Draw(dc, headRect.left, headRect.top,
							   headRect.right - headRect.left, headRect.bottom - headRect.top,
						       MERGEPAINT);
	headPose->m_drawing->Draw(dc, headRect.left, headRect.top,
							  headRect.right - headRect.left, headRect.bottom - headRect.top,
							  SRCAND);

	if (!(flags & TORSOFIRST)) {
		if ((flags & TORSOMASK) && torsoPose->m_mask)
			torsoPose->m_mask->Draw(dc, torsoRect.left, torsoRect.top,
									torsoRect.right - torsoRect.left, torsoRect.bottom - torsoRect.top,
									MERGEPAINT);
		torsoPose->m_drawing->Draw(dc, torsoRect.left, torsoRect.top,
								   torsoRect.right - torsoRect.left, torsoRect.bottom - torsoRect.top,
								   SRCAND);
	}
	return fullRect;
}

void CBodyDouble::Draw(CDC *dc, POINT *ul, RECT *dmgRect) {
	// for now, ignore ul and dmgRect
	DrawBody(dc, SRECTToRECT(m_bbox), TRUE);
}

void CBodySingle::FlipBodyBox(RECT &fullBox) {
	int temp = fullBox.left;
	fullBox.left = fullBox.right;
	fullBox.right = temp;
}

RECT CBodySingle::DrawBody(CDC *dc, RECT &clientRect, BOOL drawNimbus) {
	RECT fullRect;
	CPose *GetPoseFromID(unsigned short poseID, BOOL loadMask = TRUE);
	dc->SetStretchBltMode(STRETCH_HALFTONE); //COLORONCOLOR);
	CPose *pose = GetPoseFromID(GetPoseID(), TRUE);
	GetBodyBox(pose, clientRect, fullRect);
	if (m_flip) FlipBodyBox(fullRect);

	if (drawNimbus && pose->m_aura)
		pose->m_aura->Draw(dc, fullRect.left, fullRect.top,
						   fullRect.right - fullRect.left, fullRect.bottom - fullRect.top,
						   MERGEPAINT);
	pose->m_drawing->Draw(dc, fullRect.left, fullRect.top,
					   fullRect.right - fullRect.left, fullRect.bottom - fullRect.top,
					   SRCAND);
	return fullRect;
}

void CBodySingle::Draw(CDC *dc, POINT *ul, RECT *dmgRect) {
	DrawBody(dc, SRECTToRECT(m_bbox), TRUE);
}


void CBodyDouble::GetBodyBox(CPose *headPose, CPose *torsoPose, RECT &clientRect, RECT &fullRect, RECT &headRect, RECT &torsoRect) {

	int xOffset = m_torsoRec->xCX + m_faceRec->delta_xCX - m_faceRec->xCX;
	int yOffset = m_torsoRec->yCX + m_faceRec->delta_yCX - m_faceRec->yCX;

	RECT bitRect;
	bitRect.left = min(0, xOffset);
	bitRect.right = max(torsoPose->m_drawing->GetWidth(), xOffset + headPose->m_drawing->GetWidth());
	bitRect.top = min(0, yOffset);
	bitRect.bottom = max(torsoPose->m_drawing->GetHeight(), yOffset + headPose->m_drawing->GetHeight());


	int bitWidth = bitRect.right - bitRect.left;
	int bitHeight = bitRect.bottom - bitRect.top;
	int heightSign = (clientRect.bottom > clientRect.top) ? 1 : -1;  // heightSign < 0 if MM_TWIPS
	int clientWidth = clientRect.right - clientRect.left;
	int clientHeight = heightSign * (clientRect.bottom - clientRect.top);

	double widthScale = (double)clientWidth / bitWidth;
	double heightScale = (double)clientHeight / bitHeight;
	double scale = min(widthScale, heightScale);

	int fullHeight = ROUND(scale * bitHeight);
	int fullWidth = ROUND(scale * bitWidth);

	fullRect.left = clientRect.left + (clientWidth - fullWidth) / 2;
	fullRect.top = clientRect.top + (clientHeight - fullHeight);       // centered on bottom
	fullRect.bottom = fullRect.top + heightSign * fullHeight;
	fullRect.right = fullRect.left + fullWidth;

	headRect.left = ROUND((xOffset - bitRect.left) * scale) + fullRect.left;
	headRect.right = headRect.left + ROUND(headPose->m_drawing->GetWidth() * scale) + 1;
	headRect.top = ROUND((yOffset - bitRect.top) * scale) + fullRect.top;
	headRect.bottom = headRect.top + heightSign * (ROUND(headPose->m_drawing->GetHeight() * scale) + 1);

	torsoRect.left = ROUND((0 - bitRect.left) * scale) + fullRect.left;
	torsoRect.right = torsoRect.left + ROUND(torsoPose->m_drawing->GetWidth() * scale) + 1;
	torsoRect.top = ROUND((0 - bitRect.top) * scale) * heightSign + fullRect.top;
	torsoRect.bottom = torsoRect.top + heightSign * (ROUND(torsoPose->m_drawing->GetHeight() * scale) + 1);
}

void CBodySingle::GetBodyBox(CPose *pose, RECT &clientRect, RECT &fullRect) {
	int bitWidth = pose->m_drawing->GetWidth();
	int bitHeight = pose->m_drawing->GetHeight();
	int heightSign = (clientRect.bottom > clientRect.top) ? 1 : -1; // heightSign < 0 if MM_TWIPS
	int clientWidth = clientRect.right - clientRect.left;
	int clientHeight = heightSign * (clientRect.bottom - clientRect.top);

	double widthScale = (double)clientWidth / bitWidth;
	double heightScale = (double)clientHeight / bitHeight;

	int fullHeight, fullWidth;
	if (widthScale <= heightScale) {
		fullWidth = clientWidth;
		fullHeight = (int) (widthScale * bitHeight);
	} else {
		fullHeight = clientHeight;
		fullWidth = (int) (heightScale * bitWidth);
	}

	fullRect.left = clientRect.left + (clientWidth - fullWidth) / 2;
	fullRect.top = clientRect.top + (clientHeight - fullHeight);
	fullRect.bottom = fullRect.top + heightSign * fullHeight;
	fullRect.right = fullRect.left + fullWidth;
}

BOOL CBodyDouble::IsSame(CBody *other) {
	if (!other || GetClass() != other->GetClass()) return FALSE;
	CBodyDouble *b = (CBodyDouble *) other;	// OK, since they are of the same class
	return (m_faceRec == b->m_faceRec && m_torsoRec == b->m_torsoRec);
}

BOOL CBodySingle::IsSame(CBody *other) {
	if (!other || GetClass() != other->GetClass()) return FALSE;
	CBodySingle *b = (CBodySingle *) other;	// OK, since they are of the same class
	return (GetPoseID() == b->GetPoseID());
}


void CBodyCam::EraseRect(CDC *dc, RECT *rect) {
	dc->FillSolidRect(rect, RGB(255, 255, 255));
}

short CBodyCam::m_cursorRadius = 5;
short CBodyCam::m_iconWidth = LG_ICON_WIDTH;
short CBodyCam::m_iconHeight = LG_ICON_HEIGHT;


extern int testbody;

void CBodyCam::OnChar(UINT nChar, UINT nRepCnt, UINT nFlags) 
{
/*	if (nChar < 11) {
		testbody = nChar;
		m_avatar->DifferentTorso(testbody);
	}*/
	LPARAM lparam;
	lparam = nFlags << 16;
	lparam |= (WORD)nRepCnt;

	HWND hSayEdit = GetSay()->GetSayEdit();
	CSayCtrl* pSayCtrl = ((CSayCtrl*)FromHandle(hSayEdit));
	pSayCtrl->SendMessage(WM_CHAR,nChar,lparam);
	GetSay()->SetFocusToSayWnd();

}


void RefreshBodyCam(CAvatarX *av = NULL, BOOL refreshTalkTos = FALSE) {
	CBodyCam *bcam = GetBodyCam();
	if (av) bcam->m_avatar = av;
	if (!theApp.m_bNoRefresh) {
		bcam->RefreshBody();
		if (refreshTalkTos)
			DrawTalkTos(TRUE);
	}
}

// When MyAvatarID() == 0, bodycam logically should be detached.
void DetachBodyCamAvatar() {
	CBodyCam *bcam = GetBodyCam();
	if (bcam)
		GetBodyCam()->m_avatar = NULL;
}

BOOL RefreshBodyPreview(CAvatarX *av) {
	CBodyCam *bcam = GetCharSelBodyCam();
	if (bcam && av == bcam->m_avatar) {
		bcam->RefreshBody();
		return TRUE;
	}
	return FALSE;
}

void CBodyCam::RefreshBody() {
	CClientDC dc(this);
	m_bodyRect = DrawBody(&dc, m_avatar->m_body);
}


void CBodyCam::DrawTalkTos(CAvatarX *av, RECT *clientRect) {
	if (!m_talkToHeight) return;

	RECT localRect;
	if (!clientRect) {
		clientRect = &localRect;
		GetClientRect(clientRect);
	}

	int width = clientRect->right - clientRect->left;

	CClientDC dc(this);
	dc.FillSolidRect(0, 0, width, 2*m_talkToHeight, RGB(255, 255, 255));  // clear area

	if (width < m_talkToHeight) return;

	int upper = av->m_talkTo.GetUpperBound();
	if (upper < 0) return;

	int xStart = (width % m_talkToHeight) / 2;  // center icons horizontally
	int row = 0, col = 0;
	for (int i = 0; i <= upper; i++) {
		if ((col + 1) * m_talkToHeight >= width) {
			col = 0;
			row++;
		}
		CAvatarX *addressee = GetAvatar(av->m_talkTo[i]);
		CPose *pose = GetPoseFromID(addressee->m_icon);
		pose->m_drawing->Draw(&dc, xStart + col * m_talkToHeight, row * m_talkToHeight,
							  m_talkToHeight, m_talkToHeight, SRCCOPY);
		col++;
	}
	m_talkToRect = *clientRect;
	m_talkToRect.bottom = (col == 0) ? m_talkToRect.top : (row + 1) * m_talkToHeight;
}

void DrawTalkTos(BOOL clearFirst) {
	CBodyCam *bc = GetBodyCam();
	if (clearFirst) {
		CClientDC dc(bc);
		dc.FillSolidRect(bc->GetTalkToRect(), RGB(255, 255, 255));
	}
	CAvatarX *av = MyAvatar();
	bc->DrawTalkTos(av);
}

CPalette *CBodyCam::InstallPalette() {
	LOGPALETTE *lp = (LOGPALETTE *) malloc(sizeof(LOGPALETTE) + sizeof(PALETTEENTRY) * 255);

	lp->palVersion = 0x300;
	lp->palNumEntries = 256;
	for (int i = 0; i < 256; i++) {
		lp->palPalEntry[i].peRed = i;
		lp->palPalEntry[i].peGreen = i;
		lp->palPalEntry[i].peBlue = i;
		lp->palPalEntry[i].peFlags = 0;
	}

	ASSERT(!m_palette);  // why should we ever reinstall?
	m_palette = new CPalette();
	VERIFY(m_palette->CreatePalette(lp));
	free(lp);
	return (m_palette);
}


void CBodyCam::CreateRetainedPanel(CDC *pDC, HBITMAP *retSec, CDIB **retDib, int width, int height) {
	BYTE *pBits = NULL;
	BITMAPINFO *b = (BITMAPINFO *) malloc (sizeof(BITMAPINFOHEADER)
											   + 256*sizeof(RGBQUAD));
	BITMAPINFOHEADER *infoHdr = &(b->bmiHeader);
	infoHdr->biSize = sizeof(BITMAPINFOHEADER);
	infoHdr->biWidth = width;
	infoHdr->biHeight = height;
	infoHdr->biPlanes = 1;
	infoHdr->biBitCount = max(8, pDC->GetDeviceCaps(BITSPIXEL)); // dither if necessary, so use at least 8 bits
//	TRACE("Bodycam DPI = %d, w = %d, h = %d, bits = %d.\n", pDC->GetDeviceCaps(LOGPIXELSX), infoHdr->biWidth, infoHdr->biHeight, infoHdr->biBitCount);
	infoHdr->biCompression = BI_RGB;
	infoHdr->biSizeImage = 0;
	infoHdr->biXPelsPerMeter = 0;
	infoHdr->biYPelsPerMeter = 0;
	infoHdr->biClrUsed = 256;
	infoHdr->biClrImportant = 256;
	RGBQUAD *rgbq = (LPRGBQUAD)((BYTE*)b + sizeof(BITMAPINFOHEADER));
	for (int i = 0; i < 256; i++) {
		rgbq->rgbBlue = rgbq->rgbGreen = rgbq->rgbRed = (BYTE) i;
		rgbq->rgbReserved = 0;
		rgbq++;
	}
	*retSec = CreateDIBSection(pDC->GetSafeHdc(), b,
							   DIB_RGB_COLORS, (VOID **) &pBits,
							   NULL, 0);
	ASSERT(*retSec);
	ASSERT(pBits);
	*retDib = new CDIB;
	VERIFY((*retDib)->Create(b, pBits));
	free(b);
}


void CBodyCam::OnContextMenu(CWnd* pWnd, CPoint screenPoint) 
{
	CMenu menu;
	void UpdateFreezeUI(CMenu &);
	if(screenPoint.x == -1 && screenPoint.y == -1)  // then we must be invoking menu with Shift+F10
	{
		CRect rect; // so lets set up our own point
		GetClientRect(&rect);
		screenPoint.x = (rect.right - rect.left)/2;
		screenPoint.y = (rect.bottom - rect.top)/2;
		ClientToScreen(&screenPoint);
	}	
	menu.LoadMenu(IDR_BODYCONTEXT);
	UpdateFreezeUI(menu);
	menu.GetSubMenu(0)->TrackPopupMenu(TPM_LEFTALIGN | TPM_LEFTBUTTON | TPM_RIGHTBUTTON,
									   screenPoint.x, screenPoint.y, this);
	
}

// now toggles
void CBodyCam::OnBodycontextFreeze() 
{
	if(m_avatar)
	{
		if (m_avatar->m_freeze == AF_FROZEN)
			m_avatar->m_freeze = AF_UNFROZEN;
		else m_avatar->m_freeze = AF_FROZEN;
	//	av->m_body->m_requested = TRUE;
	}
	
}

void UpdateFreezeUI(CMenu &menu) {
	if(GetBodyCam()->m_avatar)  // check for NULLed pointer
	{
		BOOL checked = (GetBodyCam()->m_avatar->m_freeze == AF_FROZEN);
		int flag = checked ? MF_CHECKED : MF_UNCHECKED;
		menu.CheckMenuItem(ID_BODYCONTEXT_FREEZE, flag | MF_BYCOMMAND);
	}
}

#if 0
void CBodyCam::OnBodycontextUnfreeze() 
{
	m_avatar->m_freeze = AF_UNFROZEN;
//	av->m_body->m_requested = FALSE;
}
#endif

void CBodyCam::OnBodycontextSendexpression() 
{
	BOOL LegalToSend();
	if (!LegalToSend()) return;

	CString mesg = "<Chr>";
	cui.Say(mesg);	
}

void CBodyCam::OnSize(UINT nType, int cx, int cy) 
{
	CacheBullSide(cx);							// necessary for accurate RecalcRetainedBMP
	RecalcRetainedBMP();

	m_bodyRect.left = m_bodyRect.top = 0;		// initially will fill entire canvas w/ white
	m_bodyRect.right = cx;
	m_bodyRect.bottom = cy;

	CWnd::OnSize(nType, cx, cy);
}

void CBodyCam::RecalcRetainedBMP() {		
	if (m_retSec) {				// cleanup old versions
		FreeRetainedPanel();
	}
	
	RECT r;
	GetBodyRect(r);
	int height = r.bottom - r.top;
	int width = r.right - r.left;
	if (width <= 0) width = 1;
	if (height <= 0) height = 1;

	CClientDC dc(this);
	CreateRetainedPanel(&dc, &m_retSec, &m_retDib, width, height);
}

void CBodyCam::OnNcDestroy() 
{
	CWnd::OnNcDestroy();
	
	// for some reason, the main bodycam window is not destroyed automatically (why?).
	//    hence we check if it's the main window (m_forcedDelete = TRUE), and
	//	  delete it here.
	if (m_forcedDelete) {
		cui.m_pvBodyCamWnd = NULL;		// clear out bodycam pointer
		delete this;	// Otherwise destructor not called. Why is main bodycam special?
	}
}

void CBodyCam::OnUpdateBodycontextFreeze(CCmdUI* pCmdUI) 
{
	// TODO: Add your command update UI handler code here
	TRACE("CALLED BODYCONTEXT UPDATE\n");
	pCmdUI->SetCheck(TRUE);
}
extern HWND hgPrevFocus;
void CBodyCam::OnKillFocus(CWnd* pNewWnd) 
{
	CWnd::OnKillFocus(pNewWnd);
	
	hgPrevFocus = m_hWnd;
}
