#include "stdafx.h"

#include "bbox.h"
#include "pe.h"

#include "chat.h"		// for theApp
#include "dib.h"
//#include "chatprot.h"	// for AVBMPPATH
#include "vector2d.h"  // only for PI
#include "avatar.h"
#include "avatario.h"

extern CChatApp theApp;

INT16 read16(FILE *fp) inline {
	INT16 val;
	fread(&val, sizeof(val), 1, fp);
	return val;
}

INT32 read32(FILE *fp) inline {
	INT32 val;
	fread(&val, sizeof(val), 1, fp);
	return val;
}

INT8 read8(FILE *fp) inline {
	INT8 val;
	fread(&val, sizeof(val), 1, fp);
	return val;
}

CAvatarX *LoadAvatarInfo(const char *filename) {
	CString path;										// djk - BETA1 fix
	CAvatarX *av = NULL;
	FILE *fp;
	int magicNum, avType, version;

	// need to store a duplicate in AVFileRecs.  How to reclaim? Ever reclaim?
	filename = strdup(filename);

	path.Format("%s\\%s.avb", theApp.GetAvatarDir(), filename);  // djk - BETA1 fix
	if ((fp = fopen(path, "rb")) == NULL) return NULL;  // no such avatar, return NULL
	magicNum = read16(fp);
	ASSERT(magicNum == AF_MAGICNUM);
	avType = read16(fp);
	version = read16(fp);

	switch(avType) {
	case AT_COMPLEX:
		av = CAvatarComplex::LoadAvatar(fp, avType, version, filename);
		break;
	case AT_SIMPLE:
		av = CAvatarSimple::LoadAvatar(fp, avType, version, filename);
		break;
	default:
		VERIFY(0);
		break;
	}

	fclose(fp);

	if (av && av->m_name) {					// FOR NOW, IGNORE INTERNAL NAME FIELD
		free(av->m_name);
		av->m_name = strdup(filename);
	}

	return av;
}

extern char *GetToken(char *start, char **nextStart);

CAvatarX *CAvatarComplex::LoadAvatar(FILE *fp, int avType, int version, const char *path) {
	CAvatarComplex *av = new CAvatarComplex; 
	while (TRUE) {
		int key = read16(fp);
		if (av->LoadBasics(key, fp, path)) continue;
		switch(key) {
		case AK_NFACES:
			av->nFaces = read16(fp);
			av->LoadFaceRecs(fp, path);
			break;
		case AK_NTORSOS:
			av->nTorsos = read16(fp);
			av->LoadTorsoRecs(fp, path);
			break;
		case AK_STARTDATA:
			return av;
		}
	}
}

CAvatarX *CAvatarSimple::LoadAvatar(FILE *fp, int avType, int version, const char *path) {
	CAvatarSimple *av = new CAvatarSimple;
	while (TRUE) {
		int key = read16(fp);
		if (av->LoadBasics(key, fp, path)) continue;
		switch(key) {
		case AK_NBODIES:
			av->m_nBodies = read16(fp);
			av->LoadBodyRecs(fp, path);
			break;
		case AK_STARTDATA:
			return av;
		}
	}
}

BOOL CAvatarX::LoadBasics(int key, FILE *fp, const char *path) {
	switch(key) {
	case AK_NAME:
		int c;
		char buff[50], *bptr;
		bptr = buff;
		while ((c = fgetc(fp)) != EOF) {
			*bptr++ = c;
			if (!c) break;
		}
		m_name = strdup(buff);
		return TRUE;
	case AK_STYLE:
		m_style = (UCHAR) read16(fp);
		return TRUE;
	case AK_FLAGS:
		m_flags = (UCHAR) read16(fp);
		return TRUE;
	case AK_ICON:
		LoadIconRec(fp, path);
		return TRUE;
	}
	return FALSE;
}


USHORT RegisterAVFileRec(UINT fgndOffset, UINT transOffset, UINT auraOffset, const char *path);

float EmotionToFloat(int) inline;

void CAvatarComplex::LoadFaceRecs(FILE *fp, const char *path) {
	int lastOffset = 0;
	fRec = (FACEREC *) malloc (sizeof (FACEREC) * nFaces);
	for (int i = 0; i < nFaces; i++) {
//		TRACE("Loading index #%d = %ld\n", i, ftell(fp));
		int fgndOffset = read32(fp);
		int transOffset = read32(fp);
		int auraOffset = read32(fp);

		if (fgndOffset != lastOffset) {			// Non-ditto case
			fRec[i].poseID = RegisterAVFileRec(fgndOffset, transOffset, auraOffset, path);
			lastOffset = fgndOffset;
		} else
			fRec[i].poseID = fRec[i-1].poseID;  // Ditto case

		int m = read16(fp);
		fRec[i].emotion = EmotionToFloat(m);
		fRec[i].intensity = (UCHAR)read8(fp)/ (float)255;

		fRec[i].xCX = read16(fp);
		fRec[i].yCX = read16(fp);
		fRec[i].delta_xCX = read16(fp);
		fRec[i].delta_yCX = read16(fp);
		fRec[i].faceX = (UCHAR) read16(fp);
		fRec[i].faceY = (UCHAR) read16(fp);

		BYTE padding[16];
		fread(padding, 1, sizeof(padding), fp);
	}
}

void CAvatarComplex::LoadTorsoRecs(FILE *fp, const char *path) {
	int lastOffset = 0;
	bRec = (BODYREC *) malloc (sizeof (BODYREC) * nTorsos);
	for (int i = 0; i < nTorsos; i++) {
		int fgndOffset = read32(fp);
		int transOffset = read32(fp);
		int auraOffset = read32(fp);

		if (fgndOffset != lastOffset) {			// Non-ditto case
			bRec[i].poseID = RegisterAVFileRec(fgndOffset, transOffset, auraOffset, path);
			lastOffset = fgndOffset;
		} else
			bRec[i].poseID = bRec[i-1].poseID;  // Ditto case


		bRec[i].emotion = EmotionToFloat(read16(fp));
		bRec[i].intensity = (UCHAR)read8(fp) / (float)255;

		bRec[i].xCX = read16(fp);
		bRec[i].yCX = read16(fp);

		BYTE padding[16];
		fread(padding, 1, sizeof(padding), fp);
	}
}

void CAvatarSimple::LoadBodyRecs(FILE *fp, const char *path) {
	int lastOffset = 0;
	bRec = (RBODYREC *) malloc (sizeof (RBODYREC) * m_nBodies);
	for (int i = 0; i < m_nBodies; i++) {
		int fgndOffset = read32(fp);
		int transOffset = read32(fp);
		int auraOffset = read32(fp);

		if (fgndOffset != lastOffset) {			// Non-ditto case
			bRec[i].poseID = RegisterAVFileRec(fgndOffset, transOffset, auraOffset, path);
			lastOffset = fgndOffset;
		} else
			bRec[i].poseID = bRec[i-1].poseID;  // Ditto case


		bRec[i].emotion = EmotionToFloat(read16(fp));
		bRec[i].intensity = (UCHAR)read8(fp) / (float)255;

		bRec[i].faceX = (UCHAR) read16(fp);
		bRec[i].faceY = (UCHAR) read16(fp);

		BYTE padding[16];
		fread(padding, 1, sizeof(padding), fp);
	}
}

void CAvatarX::LoadIconRec(FILE *fp, const char *path) {
	int fgndOffset = read32(fp);
	m_icon = RegisterAVFileRec(fgndOffset, 0, 0, path);
}

#if 0
char *emStrings[] = {
	NULL,
	"EM_HAPPY",
	"EM_COY",
	"EM_BORED",
	"EM_SCARED",
	"EM_SAD",
	"EM_ANGRY",
	"EM_SHOUT",
	"EM_LAUGH",
	"EM_NEUTRAL",
	"EM_WAVE",
	"EM_POINTOTHER",
	"EM_POINTSELF",
	"EM_DOUBLEPOINT",
	"EM_SHRUG",
	"EM_3QRWALK",
	"EM_SIDEWALK",
	"EM_3QFWALK",
};
#endif

float emFloats[] = {
	(float) 0.0,
	(float) EM_HAPPY,
	(float) EM_COY,
	(float) EM_BORED,
	(float) EM_SCARED,
	(float) EM_SAD,
	(float) EM_ANGRY,
	(float) EM_SHOUT,
	(float) EM_LAUGH,
	(float) EM_NEUTRAL,
	(float) EM_WAVE,
	(float) EM_POINTOTHER,
	(float) EM_POINTSELF,
	(float) EM_DOUBLEPOINT,
	(float) EM_SHRUG,
	(float) EM_3QRWALK,
	(float) EM_SIDEWALK,
	(float) EM_3QFWALK,
};

float EmotionToFloat(int index) inline {
	return emFloats[index];
}

BYTE IndexToByte(BYTE);
BYTE ByteToIndex(BYTE);

void EmotionToBytes(CEmotion &em, BYTE &emotion, BYTE &intensity) {
	BYTE emVal = 9;  // neutral always safe
	int n = sizeof(emFloats) / sizeof(float);
	for (int i = 1; i < n; i++) 
		if (emFloats[i] == em.m_emotion) {
		emVal = (BYTE) i;
		break;
	}

	BYTE inVal = (BYTE)(em.m_intensity * 10);  // good 'nuff
	emotion = IndexToByte(emVal);
	intensity = IndexToByte(inVal);
}

void BytesToEmotion(CEmotion &em, BYTE emIndex, BYTE inIndex) {
	if (emIndex >= sizeof(emFloats) / sizeof(float)) em.m_emotion = EM_NEUTRAL;
	else em.m_emotion = emFloats[emIndex];
	em.m_intensity = (float)(inIndex / 10.0);
}