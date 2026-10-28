#include "stdafx.h"

#include "chat.h"
#include "bbox.h"
#include "pe.h"				// only for CPanelElement definition?
#include "dib.h"
#include "userinfo.h"
#include "avatar.h"
#include "ui.h"
#include "vector2d.h"
#include "math.h"
#include "chatprot.h"
#include "memblst.h"
#include <io.h>				// for avfile scan

extern CChatApp theApp;

void RefreshBodyCam(CAvatarX *newAv = NULL, BOOL refreshTalkTos = FALSE);
BOOL RefreshBodyPreview(CAvatarX *av);

static CPtrArray avRec;

#if 0
AVFileRec avRec[] = {
	{"browns", 128, 0, 0, 0},
	{"frog1", 129, 0, 0, 0},
};
#endif

void DeleteAVPose(int poseID, BOOL deletePath = FALSE) {
	// first flush the pose
	void FlushPoseFromID(unsigned short);
	FlushPoseFromID(poseID);

	// then remove the avRec
	AVFileRec *rec = (AVFileRec *) avRec[poseID];
	if (rec) {
		if (deletePath) free((void *)rec->filename);
		free(rec);
		avRec[poseID] = NULL;   // doesn't hold a valid AVFileRec anymore
	}
}


CAvatarX::~CAvatarX() {
	TRACE("Deleting avatar: %s.\n", m_name);
	free(m_name);				// free avatar name
	if (m_body) delete m_body;	//   and avatar body
	// delete avatar icon record...
	// note, we only delete the path associated w/ the icon's AVFileRec, since
	// it is shared by all of the avatar's AVFileRecs.
	DeleteAVPose(m_icon, TRUE);				
}

CAvatarSimple::~CAvatarSimple() {
	if (m_origID == 0) {
		for (int i = 0; i < m_nBodies; i++)
			DeleteAVPose(bRec[i].poseID);

		free(bRec);
	}
}

CAvatarComplex::~CAvatarComplex() {
	if (m_origID == 0) {
		for (int i = 0; i < nFaces; i++)
			DeleteAVPose(fRec[i].poseID);
		free(fRec);

		for (i = 0; i < nTorsos; i++)
			DeleteAVPose(bRec[i].poseID);
		free(bRec);
	}
}



USHORT RegisterAVFileRec(UINT fgndOffset, UINT transOffset, UINT auraOffset, const char *path) {
	AVFileRec *arec = (AVFileRec *) malloc (sizeof(AVFileRec));
	arec->fgndOffset = fgndOffset;
	arec->transOffset = transOffset;
	arec->auraOffset = auraOffset;
	arec->filename = path;

	return avRec.Add(arec);
}



// this assumes that m_poseID is also the avRec index
void CBodySingle::GetDimInfo(short &xdim, short &ydim, short &normHeight, short &headHeight, short &faceX) {
	CPose *pose = GetPoseFromID(GetPoseID());
	xdim = pose->m_drawing->GetWidth();
	ydim = pose->m_drawing->GetHeight();
	headHeight = ydim/2;	// for now, be conservative -- head = half body!
	normHeight = 100;       // rec->normHeight;  -- XXX right now, we're setting normHeight to a constant. Fix?!

	CAvatarSimple *av = (CAvatarSimple *) GetAvatar(m_avatarID);
	faceX = av->bRec->faceX;
	if (m_flip) faceX = xdim - faceX;
}

void CBodyDouble::GetDimInfo(short &xdim, short &ydim, short &normHeight, short &headHeight, short &faceX) {
	// this code is adapted from GetBodyBox, does a little less, but is faster
	CPose *head = GetPoseFromID(m_faceRec->poseID);
	CPose *torso = GetPoseFromID(m_torsoRec->poseID);
	int xOffset = m_torsoRec->xCX + m_faceRec->delta_xCX - m_faceRec->xCX;
	int yOffset = m_torsoRec->yCX + m_faceRec->delta_yCX - m_faceRec->yCX;

	RECT bitRect;
	bitRect.left = min(0, xOffset);
	bitRect.right = max(torso->m_drawing->GetWidth(), xOffset + head->m_drawing->GetWidth());
	bitRect.top = min(0, yOffset);
	headHeight = yOffset + head->m_drawing->GetHeight();
	bitRect.bottom = max(torso->m_drawing->GetHeight(), headHeight);

	xdim = (short)(bitRect.right - bitRect.left);
	ydim = (short)(bitRect.bottom - bitRect.top);
	headHeight -= (short)bitRect.top;
	normHeight = 100;  /// XXX right now, we're assuming characters are the same height

	faceX = (short) (m_faceRec->faceX + xOffset - bitRect.left);
	if (m_flip) faceX = xdim - faceX;
}


CMapWordToPtr poseMap(22);		// maps poseID to pose

CPose *GetPoseFromID(unsigned short poseID, BOOL loadMask) {
	void *p;
	FILE *fp;

	if (poseMap.Lookup(poseID, p)) {		// is it already there?
		CPose *pi = (CPose *) p;
		return pi;
	}
	TRACE("Allocating id %d.\n", poseID);

	// Nope, we need to construct a pose, and register it
	CPose *pose = new CPose;

	char buff[100];
	AVFileRec *arec = (AVFileRec *) avRec[poseID];

	sprintf(buff, "%s\\%s.avb", theApp.GetAvatarDir(), arec->filename);
	VERIFY(fp = fopen(buff, "rb"));
	pose->m_drawing = new CDIB;
	fseek(fp, (long)arec->fgndOffset, SEEK_SET);
	VERIFY(pose->m_drawing->Load(fp));

	if (loadMask) {
		if (arec->transOffset) {
			pose->m_mask = new CDIB;
			fseek(fp, (long)arec->transOffset, SEEK_SET);
			if (!pose->m_mask->Load(fp)) {
				delete pose->m_mask;
				pose->m_mask = NULL;
			}
		}
		if (arec->auraOffset) {
			pose->m_aura = new CDIB;
			fseek(fp, (long)arec->auraOffset, SEEK_SET);
			if (!pose->m_aura->Load(fp)) {
				delete pose->m_aura;
				pose->m_aura = NULL;
			}
		}
	} 
	fclose(fp);

	poseMap.SetAt(poseID, pose);
	return pose;
}

void FlushPoseFromID(unsigned short poseID) {
	void *p;
	if (poseMap.Lookup(poseID, p)) {		// is it really there?
		poseMap.RemoveKey(poseID);
		CPose *pi = (CPose *) p;
		delete pi;
	}
}

void CAvatarX::GetAvatarName(const char **name, const char **nickname) {
//	ASSERT(avatarID != 0 && avatarID < (sizeof(avatars) / sizeof(CAvatarX *)));
	CUserInfo *pui = (CUserInfo *) m_userInfo;
	ASSERT(pui);
	*name = m_name;
	*nickname = (LPCTSTR) pui->GetName();
}

void GetUserName(UINT avatarID, const char **name, const char **nickname) {
	CAvatarX *av = GetAvatar(avatarID);
	CUserInfo *pui = (CUserInfo *) av->m_userInfo;
	ASSERT(pui);
	*name = (LPCTSTR) pui->GetName();
	*nickname = (LPCTSTR) pui->GetFullName();
}


CPose::~CPose() {
	if (m_drawing) delete m_drawing;
	if (m_mask) delete m_mask;
	if (m_aura) delete m_aura;
}


#define DEFAULTBODY	0
int testbody = DEFAULTBODY;

CBody *CAvatarSimple::GetBodyFromEmotion(CEmotion &emotion) {
	CBodySingle *body = new CBodySingle(m_avatarID);

	// Distance metric needs rethinking!
	double nearestAngle = 3*PI;
	double intensityOfNearest = 2.0;
	int nearestI = -1;

	for (int i = 0; i < m_nBodies; i++) {
		int index = (m_lastBody + 1 + i) % m_nBodies;  // start search from index after last body used
		if (bRec[index].emotion > 7) continue;
		double thisAngle = fabs(subtract_angles(bRec[index].emotion, emotion.m_emotion));
		BOOL isFirstNeutral = bRec[index].emotion == EM_NEUTRAL && bRec[index].intensity == 0.0 && nearestI == -1; // consider first neutral if necessary
		if (thisAngle < PI / NEMOTIONS  || isFirstNeutral) {   // from 2PI / (2*NEMOTIONS), since can go half dist in each direction
			double delta_i;
			if (isFirstNeutral && emotion.m_intensity > 0.0) delta_i = 1.5; // less powerful than any correct match
			else delta_i = fabs(emotion.m_intensity - bRec[index].intensity);
			if (delta_i < intensityOfNearest) {
				nearestAngle = thisAngle;
				intensityOfNearest = delta_i;
				nearestI = index;
			}
		}
	}

	SetBody(body, nearestI);
	return body;	
}


CBody *CAvatarComplex::GetBodyFromEmotion(CEmotion &emotion) {
	CBodyDouble *body = new CBodyDouble(m_avatarID);
//	SetTorso(body, testbody);

	// Distance metric needs rethinking!
	double nearestAngle = 3*PI;
	double intensityOfNearest = 2.0;
	int nearestI;

	for (int i = 0; i < nFaces; i++) {
		double thisAngle = fabs(subtract_angles(fRec[i].emotion, emotion.m_emotion));
		if (thisAngle <= nearestAngle) {
			double delta_i = fabs(emotion.m_intensity - fRec[i].intensity);
			if (thisAngle == nearestAngle && delta_i >= intensityOfNearest)
				continue;
			nearestAngle = thisAngle;
			intensityOfNearest = delta_i;
			nearestI = i;
		}
	}

	SetFace(body, nearestI);
//	TRACE("Face = %d.\n", nearestI);


	intensityOfNearest = 2.0;
	for (i = 0; i < nTorsos; i++) {
		int index = (m_lastTorso + 1 + i) % nTorsos;  // start search from index after last body used
		if (bRec[index].emotion > 7) continue;
		double thisAngle = fabs(subtract_angles(bRec[index].emotion, emotion.m_emotion));
//		TRACE("%d: input = %f, body = %f, thisAngle = %f\n", i, emotion.m_emotion, bRec[i].emotion, thisAngle);
		if (thisAngle < PI / NEMOTIONS  ||    // from 2PI / (2*NEMOTIONS), since can go half dist in each direction
			bRec[index].emotion == EM_NEUTRAL) { // consider neutrals, too
			double delta_i = fabs(emotion.m_intensity - bRec[index].intensity);
			if (delta_i < intensityOfNearest) {
				intensityOfNearest = delta_i;
				nearestI = index;
			}
		}
	}
//	TRACE("Nearest I = %d.\n", nearestI);
	SetTorso(body, nearestI);
//	TRACE("TORSO = %d.\n", nearestI);

	return body;	
}

void CAvatarComplex::GetHeadAndBodyFromEmotion(CEmotion &emotion, int& fIndex, int& tIndex) {
	// Distance metric needs rethinking!
	double nearestAngle = 3*PI;
	double intensityOfNearest = 2.0;
	fIndex = tIndex = -1;

	if (emotion.m_emotion <= 2*PI) {		// Otherwise, can't use this metric
		for (int i = 0; i < nFaces; i++) {
			double thisAngle = fabs(subtract_angles(fRec[i].emotion, emotion.m_emotion));
			if (thisAngle <= nearestAngle) {
				double delta_i = fabs(emotion.m_intensity - fRec[i].intensity);
				if (thisAngle == nearestAngle && delta_i >= intensityOfNearest)
					continue;
				nearestAngle = thisAngle;
				intensityOfNearest = delta_i;
				fIndex = i;
			}
		}
	} else {
		for (int i = 0; i < nTorsos; i++) {
			if (emotion.m_emotion == bRec[i].emotion) {
				tIndex = i;
				break;
			}
		}
	}
}

void CAvatarSimple::GetBodyIndexFromEmotion(CEmotion &emotion, int& bIndex) {
	// Distance metric needs rethinking!
	double nearestAngle = 3*PI;
	double intensityOfNearest = 2.0;
	bIndex = -1;

	if (emotion.m_emotion <= 2*PI) {		// Otherwise, can't use this metric
		for (int i = 0; i < m_nBodies; i++) {
			double thisAngle = fabs(subtract_angles(bRec[i].emotion, emotion.m_emotion));
			if (thisAngle <= nearestAngle) {
				double delta_i = fabs(emotion.m_intensity - bRec[i].intensity);
				if (thisAngle == nearestAngle && delta_i >= intensityOfNearest)
					continue;
				nearestAngle = thisAngle;
				intensityOfNearest = delta_i;
				bIndex = i;
			}
		}
	} else {
		for (int i = 0; i < m_nBodies; i++) {
			if (emotion.m_emotion == bRec[i].emotion) {
				bIndex = i;
				break;
			}
		}
	}
}

CBody *CAvatarComplex::GetBodyFromEmotion(CEmotionOpts &emOpts) {
	int bestIndex, fIndex, tIndex, foundF = -1, foundT = -1;
	UCHAR minPriority;
	CBodyDouble *body = new CBodyDouble(m_avatarID);

	while (TRUE) {
		minPriority = 0;
		for (int i = 0; i < emOpts.m_nOpts; i++) {
			if (emOpts.m_priorities[i] > minPriority) {
				bestIndex = i;
				minPriority = emOpts.m_priorities[i];
			}
		}
		if (!minPriority) break;
		GetHeadAndBodyFromEmotion(emOpts.m_emotions[bestIndex], fIndex, tIndex);
		emOpts.m_priorities[bestIndex] = 0;   // nuke this entry, so we don't kill again
		if (fIndex >= 0 && foundF < 0) {
			SetFace(body, fIndex);
			foundF = fIndex;
		}
		if (tIndex >= 0 && foundT < 0) {
			SetTorso(body, tIndex);
			foundT = tIndex;
		}
		if (foundF >= 0 && foundT >= 0) break;	// filled constraints -- don't need to continue
	}
	if (foundF < 0) SetFaceNeutral(body);
	if (foundT < 0) SetTorsoNeutral(body);
//	TRACE("Face: %d.  Torso: %d.\n", body->m_faceRec - fRec, body->m_torsoRec - bRec);
	return body;
}

CBody *CAvatarSimple::GetBodyFromEmotion(CEmotionOpts &emOpts) {;
	int bestIndex, bIndex, foundB = -1;
	UCHAR minPriority;
	CBodySingle *body = new CBodySingle(m_avatarID);

	// find first exact match w/ highest priority
	while (TRUE) {
		minPriority = 0;
		for (int i = 0; i < emOpts.m_nOpts; i++) {
			if (emOpts.m_priorities[i] > minPriority) {
				bestIndex = i;
				minPriority = emOpts.m_priorities[i];
			}
		}
		if (!minPriority) break;
		GetBodyIndexFromEmotion(emOpts.m_emotions[bestIndex], bIndex);
		emOpts.m_priorities[bestIndex] = 0;   // nuke this entry, so we don't kill again
		if (bIndex >= 0 && foundB < 0) {
			SetBody(body, bIndex);
			foundB = bIndex;
			break;
		}
	}
	if (foundB < 0) SetBodyNeutral(body);
//	TRACE("Face: %d.  Torso: %d.\n", body->m_faceRec - fRec, body->m_torsoRec - bRec);
	return body;
}


void CAvatarComplex::SetTorsoNeutral(CBodyDouble *body) {
	int c = m_lastTorso;
	for (int i = 0; i < nTorsos; i++) {
		c = (c+1) % nTorsos;
		if (bRec[c].emotion == EM_NEUTRAL && bRec[c].intensity == 0.0) {
			SetTorso(body, c);
			return;
		}
	}

	SetTorso(body, 0);		// Oh well, just set it to first
}

void CAvatarComplex::SetFaceNeutral(CBodyDouble *body) {
	int c = m_lastFace;
	for (int i = 0; i < nFaces; i++) {
		c = (c+1) % nFaces;
		if (fRec[c].emotion == EM_NEUTRAL && fRec[c].intensity == 0.0) {
			SetFace(body, c);
			return;
		}
	}

	SetFace(body, 0);		// Oh well, just set it to first
}

void CAvatarSimple::SetBodyNeutral(CBodySingle *body) {
	int c = m_lastBody;
	for (int i = 0; i < m_nBodies; i++) {
		c = (c+1) % m_nBodies;
		if (bRec[c].emotion == EM_NEUTRAL && bRec[c].intensity == 0.0) {
			SetBody(body, c);
			return;
		}
	}

	SetBody(body, 0);		// Oh well, just set it to first
}

void ResetAvatar(int avID) {
	CAvatarX *av = GetAvatar(avID);
	if (av->m_freeze == AF_TEMPFROZEN)		// Reset temporary freezes to unfrozen
		av->m_freeze = AF_UNFROZEN;
	if (av->m_freeze == AF_UNFROZEN)		// Make unfrozen avatars neutral
		av->SetNeutral();
}


void CAvatarComplex::SetNeutral() {
	CBodyDouble *body = new CBodyDouble(m_avatarID);
	SetTorsoNeutral(body);
	SetFaceNeutral(body);
	body->m_requested = FALSE;
	UpdateBody(body);
}

void CAvatarSimple::SetNeutral() {
	CBody *body = GetBodyFromEmotion(CEmotion(0.0, 0.0)); // creates the only body that's known...
	body->m_requested = FALSE;
	UpdateBody(body);
}

void CAvatarX::UpdateBody(CBody *newBody) {
	if (newBody->IsSame(m_body)) 
		delete newBody;
	else {
		if (m_body) delete m_body;
		m_body = newBody;

		if (!RefreshBodyPreview(this)) // will check for validity
			if (m_avatarID == MyAvatarID())
				RefreshBodyCam();
	}
}


void CAvatarComplex::DifferentTorso(int torsoIndex) {
	if (m_body->GetClass() == BC_BODYDOUBLE) {
		CBodyDouble *newBody = new CBodyDouble;
		*newBody = *((CBodyDouble *) m_body);
		newBody->m_torsoRec = bRec + torsoIndex;
		UpdateBody(newBody);
	}
}


UINT myAvatarID = 0;

static CPtrArray avatars;   // + 1 for NULL

int GetAvatarUpperBound () inline {
	return avatars.GetUpperBound();
}

CAvatarX *MyAvatar() {
	return ((CAvatarX *) avatars[myAvatarID]);
}

UINT MyAvatarID() {
	return myAvatarID;
}


void InitializeAvatars() {
	avRec.SetSize(0, 200);
	avRec.Add(NULL);
	avatars.SetSize(0, 100);
	avatars.Add(NULL);				// first entry is NULL
}

void DestroyAvatars() {
	for (int i = GetAvatarUpperBound(); i >= 1; i--) {
		CAvatarX *av = (CAvatarX *) avatars[i];
		if (av) delete av;
	}

	avatars.SetSize(1);  // First element, NULL, retained
	avRec.SetSize(1);	  // First element, NULL retained
	poseMap.RemoveAll();  // individual poses freed as consequence of delete av;
}

CAvatarX *LoadAvatar(const char *avName) {
	// for now, assumes it's not been loaded!
	CAvatarX *LoadAvatarInfo(const char *);
	CAvatarX *av = LoadAvatarInfo(avName);
	if (!av) return NULL;    // invalid avName
	return(av->IndexAvatar());
}

CAvatarX *CAvatarX::IndexAvatar() {
	m_avatarID = avatars.Add(this);
	SetNeutral();
	return this;
}

#if 0
	avatars[10] = MakeAvatar_Simple("GreekChorus", "kibbitz", 10, 0, 129, 97, 40);


	// Give all of the avatars a neutral expression by default.  Note that this has to
	// be done after setting avatars[] array, since SetNeutral may refresh BodyCam, which
	// may require a BodyDraw, which may require a GetAvatar, which may require the avatars
	// array to be set.
	for (int i = 1; i <= NAVATARS; i++) {
		avatars[i]->SetNeutral();
		avatars[i]->m_lastRight = i-1;
		avatars[i]->m_lastLeft = i+1;
		avatars[i]->m_lastDir = TRUE;  // FALSE - SIGGRAPH PIX
	}
	
}
#endif

CAvatarX *GetAvatar(USHORT avatarID) {
	return ((CAvatarX *) avatars[avatarID]);
}

CAvatarX *GetAvatar(const char *name) {		// perhaps should have a hash table to map names to avatars
	int upperBound = avatars.GetUpperBound();
	for (int i = 1; i <= upperBound; i++) {
		if (!stricmp(((CAvatarX *) avatars[i])->m_name, name))
			return ((CAvatarX *) avatars[i]);
	}
	return NULL;
}

void SetMyAvatar(UINT avID) {
	void SetMyPUIAvatarID(UINT);		// sets the avatarID in my CUserInfo
	extern void SetMyCharacter(const char *);

	if (avID != MyAvatarID()) {
		myAvatarID = avID;
		SetMyPUIAvatarID(avID);
		CAvatarX *av = MyAvatar();
		RefreshBodyCam(av, TRUE);
		const char *originalName = av->OriginalName();
		SetMyCharacter(originalName);	    // duplicated info -- arg.
		if (!theApp.m_bNoRefresh) ChatAnnounceNewAvatar(originalName);
	}
}

BOOL SetMyAvatar(const char *avName) {
	extern CUserInfo *puiSelf;

	myAvatarID = 0;  // force reset in SetMyAvatar(UINT)

	CAvatarX *av = GetAvatar3(avName, puiSelf);
	ASSERT(av);
#if 0
	if (!av) {  // bad name -- choose another randomly
		char randName[50];
		void GetNextAvatarName(char *);
		GetNextAvatarName(randName);
		av = GetAvatar3(randName, puiSelf);
	}
#endif

	SetMyAvatar(av->m_avatarID);
	return TRUE;
}

static int nextAvatarName = -1;
static CStringArray strFiles;

void GetAllAvatarNames() {
// build file search strings
    CString strPattern;
	struct _finddata_t fd;
	long hFind;

    strFiles.RemoveAll();
	strPattern = theApp.GetAvatarDir();
	strPattern += "\\*.avb";
	hFind = _findfirst( (char *) (const char *) strPattern, &fd );
	if( hFind != -1L )
	{
		do 
		{
			if (fd.attrib != _A_SUBDIR)
			{
				char szExt[_MAX_EXT];
				char szFName[_MAX_FNAME];
				
				_splitpath( fd.name, NULL, NULL, szFName, szExt );
				strFiles.Add(szFName);
		   }

		} while( _findnext( hFind, &fd ) != -1 );
	}
}

	
void GetNextAvatarName(char *avName) {
	if (nextAvatarName == -1)
		GetAllAvatarNames();
	int upperBound = strFiles.GetUpperBound();
	nextAvatarName++;
    if (nextAvatarName > upperBound)
		nextAvatarName = 0;
	if (MyAvatar() && !stricmp(strFiles[nextAvatarName],MyAvatar()->m_name) && (upperBound > 1)) {
			GetNextAvatarName(avName);
			return;
		}
	strcpy(avName, (strFiles[nextAvatarName]));
}


CAvatarX *GetAvatar2(const char *avName) {
	int upperBound = avatars.GetUpperBound();
	for (int i = 1; i <= upperBound; i++) {
		if (!stricmp(((CAvatarX *)avatars[i])->m_name, avName)) {
			return (CAvatarX *) avatars[i];
		}
	}
	return (LoadAvatar(avName));
}

// pui is void * so others need not know about CUserInfo
CAvatarX *GetAvatar3(const char *avName, void *pui) {
	CAvatarX *orig = NULL;
	int upperBound = avatars.GetUpperBound();
	for (int i = 1; i <= upperBound; i++) {
		CAvatarX *av = (CAvatarX *) avatars[i];
		if (!stricmp(av->OriginalName(), avName)) {
			if (av->m_userInfo == NULL || av->m_userInfo == pui) {
				av->m_flags &= ~OTHERMAPPED;
				return av; // it's not being used! return the original
			}
			else if (av->m_origID == 0) orig = av;
		}
	}

	if (orig) {  // found the original, but it's being used.  So dup it.
		CAvatarX *newAv = orig->DupAvatar();
		newAv->IndexAvatar();
		newAv->m_flags &= ~OTHERMAPPED;
		return newAv;
	}

	CAvatarX *newAv = LoadAvatar(avName);
	if (!newAv) {
		char avatarstr[256];
		GetNextAvatarName(avatarstr);
		newAv = GetAvatar3(avatarstr, pui);
		newAv->m_flags |= OTHERMAPPED;
	}
	return newAv;
}


CBodySingle::CBodySingle(const CBodySingle &b)
:CBody(b) {
	m_bodyRec = b.m_bodyRec;
}

CBodyDouble::CBodyDouble(const CBodyDouble &b)
:CBody(b) {
	m_faceRec = b.m_faceRec;
	m_torsoRec = b.m_torsoRec;
}

CBodyUnary::CBodyUnary(const CBodyUnary &b)
:CBodySingle(b) {
	m_bodyID = b.m_bodyID;
}

void CEmotionOpts::Add(double emotion, double intensity, int priority, int flags) {
	for (int i = 0; i < m_nOpts; i++) {
		if (m_emotions[i].m_emotion == emotion) {
			if (flags & OVERRIDEBYPRIORITY) {
				if (m_priorities[i] < priority) {
					m_priorities[i] = priority;
					m_emotions[i].m_intensity = (float)intensity;
				}
				return;
			} else if (flags & ADDPRIORITY) {
				m_priorities[i] = max(m_priorities[i] + priority, 255);
				m_emotions[i].m_intensity = (float) max(m_emotions[i].m_intensity, intensity);
				return;
			}
		}
	}

	if (m_nOpts >= MAXEMOPTS) return;

	// otherwise add a new entry
	m_emotions[m_nOpts].m_emotion = (float) emotion;
	m_emotions[m_nOpts].m_intensity = (float) intensity;
	m_priorities[m_nOpts++] = priority;
}

void CAvatarComplex::RecordBody(CBody *bdy) {
	switch (bdy->GetClass()) {
		case BC_BODYDOUBLE:			// only care about this case
			CBodyDouble *d = (CBodyDouble *) bdy;
			m_lastFace = d->m_faceRec - fRec;
			m_lastTorso = d->m_torsoRec - bRec;
	}
}

void CAvatarSimple::RecordBody(CBody *bdy) {
	switch(bdy->GetClass()) {
		case BC_BODYSINGLE:
			CBodySingle *d = (CBodySingle *) bdy;
			m_lastBody = d->m_bodyRec - bRec;
	}
}

void CAvatarX::SelectTalkTo(UINT avID, BOOL extend, BOOL updateMemberList) {
	updateMemberList = updateMemberList && (m_avatarID == MyAvatarID());
	BOOL itsMe = (avID && (GetAvatar(avID)->m_userInfo == m_userInfo));

	if (!extend) {							// plain selection
		m_talkTo.RemoveAll();
		if (avID && !itsMe)	// if it's not background, and not self, add
			m_talkTo.Add(avID);
		else if (!avID) {
			if (updateMemberList) UnselectAll();
			return;
		}
		if (updateMemberList)	{			// update selections in member pane
			UnselectAll();
			SelectInMemberList(avID);
		}
	} else {
		if (!avID) return;	// only add an avatar if the cursor is over one
		if (!itsMe) {
			int topIndex = m_talkTo.GetUpperBound();
			for (int i = 0; i <= topIndex; i++)
				if (m_talkTo[i] == avID) return;
			m_talkTo.Add(avID);
		}
		if (updateMemberList) SelectInMemberList(avID);
	}
}

void CAvatarX::DeselectTalkTo(UINT avID, BOOL updateMemberList) {
	if (!avID) return;		// probably will never occur
	int topIndex = m_talkTo.GetUpperBound();
	for (int i = 0; i <= topIndex; i++) {
		if (m_talkTo[i] == avID) {
			m_talkTo.RemoveAt(i);
			break;
		}
	}

	updateMemberList = updateMemberList && (m_avatarID == MyAvatarID());
	if (updateMemberList) SelectInMemberList(avID, FALSE);
}


void CAvatarX::UnselectAll()
{
	int nCount = GetMembers()->m_MemberListBox.GetItemCount();
	for(int index = 0; index < nCount; index++)
	{
		UINT state = GetMembers()->m_MemberListBox.GetItemState(index, LVIS_SELECTED | LVIS_FOCUSED );
		state &= ~LVIS_SELECTED & ~LVIS_FOCUSED;
		BOOL bResult = GetMembers()->m_MemberListBox.SetItemState( index, state, LVIS_SELECTED | LVIS_FOCUSED);
	}
}

void CAvatarX::SelectInMemberList(UINT avID, BOOL select)
{
	int FindMemberListIndex(CUserInfo *);
	CUserInfo *pui = (CUserInfo *) (GetAvatar(avID)->m_userInfo);
	int index = FindMemberListIndex(pui);

	UINT state = GetMembers()->m_MemberListBox.GetItemState(index, LVIS_SELECTED | LVIS_FOCUSED );
	if (select) state |= LVIS_SELECTED | LVIS_FOCUSED;
	else state &= ~(LVIS_SELECTED | LVIS_FOCUSED);
	BOOL bResult = GetMembers()->m_MemberListBox.SetItemState( index, state, LVIS_SELECTED | LVIS_FOCUSED);
}

void CAvatarX::ClearTalkTos() {
	m_talkTo.RemoveAll();
	if (m_avatarID == MyAvatarID())
		DrawTalkTos(TRUE);
}

void CAvatarComplex::GetIndices(UCHAR &faceIndex, UCHAR &torsoIndex, UCHAR &requested) {
	ASSERT(m_body->GetClass() == BC_BODYDOUBLE);
	CBodyDouble *b = (CBodyDouble *) m_body;
	faceIndex = b->m_faceRec - fRec;
	torsoIndex = b->m_torsoRec - bRec;
	requested = m_freeze != AF_UNFROZEN;
}

void CAvatarSimple::GetIndices(UCHAR &faceIndex, UCHAR &torsoIndex, UCHAR &requested) {
	ASSERT(m_body->GetClass() == BC_BODYSINGLE);
	CBodySingle *b = (CBodySingle *) m_body;
	torsoIndex = b->m_bodyRec - bRec;
	faceIndex = (UCHAR) 0;
	requested = m_freeze != AF_UNFROZEN;
}

void CAvatarComplex::SetIndices(UCHAR faceIndex, UCHAR torsoIndex, UCHAR requested) {
	ASSERT(m_body->GetClass() == BC_BODYDOUBLE);
	CBodyDouble *b = (CBodyDouble *) m_body;
	if (faceIndex >= 0 && faceIndex < nFaces)
		b->m_faceRec = fRec + faceIndex;
	if (torsoIndex >= 0 && torsoIndex < nTorsos) 
		b->m_torsoRec = bRec + torsoIndex;
	b->m_requested = requested; 
}

void CAvatarSimple::SetIndices(UCHAR faceIndex, UCHAR torsoIndex, UCHAR requested) {
	ASSERT(m_body->GetClass() == BC_BODYSINGLE);
	CBodySingle *b = (CBodySingle *) m_body;
	if (torsoIndex >= 0 && torsoIndex < m_nBodies) 
		b->m_bodyRec = bRec + torsoIndex;
	b->m_requested = requested; 
}

void CAvatarComplex::GetEmotions(CEmotion &face, CEmotion &torso) {
	ASSERT(m_body->GetClass() == BC_BODYDOUBLE);
	CBodyDouble *b = (CBodyDouble *) m_body;
	face.m_emotion = b->m_faceRec->emotion;
	face.m_intensity = b->m_faceRec->intensity;
	torso.m_emotion = b->m_torsoRec->emotion;
	torso.m_intensity = b->m_torsoRec->intensity;
}

void CAvatarSimple::GetEmotions(CEmotion &face, CEmotion &torso) {
	ASSERT(m_body->GetClass() == BC_BODYSINGLE);
	CBodySingle *b = (CBodySingle *) m_body;
	face.m_emotion = b->m_bodyRec->emotion;
	face.m_intensity = b->m_bodyRec->intensity;
}

void CAvatarSimple::SetEmotions(CEmotion &face, CEmotion &torso) {
	ASSERT(m_body->GetClass() == BC_BODYSINGLE);
	CBodySingle *b = (CBodySingle *) m_body;
	// try to match emotion, w/ closest intensity
	double bestDelta = 1000.0;
	int index, endIndex, bestIndex = -1;
	index = endIndex = b->m_bodyRec - bRec;
	do {
		index = (index + 1) % m_nBodies;
		if (bRec[index].emotion == face.m_emotion) {
			float delta = (float) fabs(bRec[index].intensity - face.m_intensity);
			if (delta < bestDelta) {
				bestDelta = delta;
				bestIndex = index;
			}
		}
	} while (index != endIndex);
	if (bestIndex >= 0) b->m_bodyRec = bRec + bestIndex;
	else SetBodyNeutral(b);
}

void CAvatarComplex::SetEmotions(CEmotion &face, CEmotion &torso) {
	ASSERT(m_body->GetClass() == BC_BODYDOUBLE);
	CBodyDouble *b = (CBodyDouble *) m_body;

	// try to match face emotion, w/ closest intensity
	double bestDelta = 1000.0;
	int index, endIndex, bestIndex = -1;
	index = endIndex = b->m_faceRec - fRec;
	do {
		index = (index + 1) % nFaces;
		if (fRec[index].emotion == face.m_emotion) {
			float delta = (float) fabs(fRec[index].intensity - face.m_intensity);
			if (delta < bestDelta) {
				bestDelta = delta;
				bestIndex = index;
			}
		}
	} while (index != endIndex);
	if (bestIndex >= 0) b->m_faceRec = fRec + bestIndex;
	else SetFaceNeutral(b);

	// try to match body gesture, w/ closest intensity
	bestDelta = 1000.0;
	index = endIndex = b->m_torsoRec - bRec;
	bestIndex = -1;
	do {
		index = (index + 1) % nTorsos;
		if (bRec[index].emotion == torso.m_emotion) {
			float delta = (float) fabs(bRec[index].intensity - torso.m_intensity);
			if (delta < bestDelta) {
				bestDelta = delta;
				bestIndex = index;
			}
		}
	} while (index != endIndex);
	if (bestIndex >= 0) b->m_torsoRec = bRec + bestIndex;
	else SetTorsoNeutral(b);
}

static int nextInstanceID = 1;

CAvatarX::CAvatarX(const CAvatarX &b) {
	Initialize();
	char name[100];
	sprintf(name, "%s#%d", b.m_name, b.m_nCopies);
	m_name = strdup(name);
	m_origID = b.m_avatarID;
	m_flags = b.m_flags;
	m_icon = b.m_icon;				   // poseID for icon		
}

CAvatarSimple::CAvatarSimple(const CAvatarSimple &b) : CAvatarX(b) {
	bRec = b.bRec;
	m_nBodies = b.m_nBodies;
	m_lastBody = -1;
}

CAvatarComplex::CAvatarComplex(const CAvatarComplex &b) : CAvatarX(b) {
	fRec = b.fRec;
	bRec = b.bRec;
	nFaces = b.nFaces;
	nTorsos = b.nTorsos;
	m_lastFace = m_lastTorso = -1;
}

int TalkToCount() {
	return (MyAvatar()->m_talkTo.GetUpperBound() + 1);
}

const char *CAvatarX::OriginalName() {
	if (!m_origID) return m_name;
	else return GetAvatar(m_origID)->m_name;
}

BOOL NullAvatar() {
	return (MyAvatar() == NULL);
}

