class CPose {
public:
	CDIB *m_drawing;
	CDIB *m_mask;
	CDIB *m_aura;

	CPose() { m_drawing = NULL; m_mask = NULL; m_aura = NULL; }
	~CPose();
};

class CEmotion {
public:
	CEmotion() {}
	CEmotion(double intensity, double emotion) { m_intensity = (float) intensity; m_emotion = (float) emotion; }
	void Set(double intensity, double emotion) { m_intensity = (float) intensity; m_emotion = (float) emotion; }
	float m_intensity;
	float m_emotion;
};

#define MAXEMOPTS	10
#define OVERRIDEBYPRIORITY	1
#define ADDPRIORITY	2

class CEmotionOpts {
public:
	UCHAR m_nOpts;
	CEmotion m_emotions[MAXEMOPTS];
	UCHAR m_priorities[MAXEMOPTS];

	CEmotionOpts() { m_nOpts = (UCHAR) 0; }
	void Add(int emotion, double intensity, int priority, int flags = OVERRIDEBYPRIORITY) { Add((double) emotion, intensity, priority, flags); }
	void Add(double emotion, double intensity, int priority, int flags = OVERRIDEBYPRIORITY);
};

class CBody : public CPanelElement {
public:
	UINT m_avatarID;
	UCHAR m_flip;
	UCHAR m_requested;
	short m_arrowX;
	CBody() {}
	CBody(unsigned int avID) { m_avatarID = avID; m_flip = FALSE; m_requested = TRUE; }
	CBody(const CBody &b) : CPanelElement(b) { m_avatarID = b.m_avatarID; m_flip = b.m_flip; m_requested = b.m_requested; }
	virtual BOOL IsSame(CBody *other) = 0;
	virtual UCHAR GetClass() = 0;
	virtual RECT DrawBody(CDC *dc, RECT &clientArea, BOOL drawNimbus) = 0;
	virtual CBody *Clone() = 0;
	virtual void GetDimInfo(short &xdim, short &ydim, short &normHeight, short &headHeight, short &faceX) = 0;
//	virtual POINT GetFaceCenter() { POINT pt; pt.x = (m_bbox.Left + m_bbox.Right)/2; pt.y = (m_bbox.Top + m_bbox.Bottom)/2; return pt; }  // bad estimate
};

#define BC_BODYDOUBLE	1
#define BC_BODYSINGLE	2

typedef struct {
	unsigned short poseID;
	float emotion;
	float intensity;
	short xCX;
	short yCX;
	short delta_xCX;
	short delta_yCX;
	UCHAR faceX;
	UCHAR faceY;
} FACEREC;

typedef struct {
	unsigned short poseID;
	float emotion;
	float intensity;
	short xCX;
	short yCX;
} BODYREC;

typedef struct {
	unsigned short poseID;
	float emotion;
	float intensity;
	UCHAR faceX;
	UCHAR faceY;
} RBODYREC;


class CBodyDouble: public CBody {
public:
	FACEREC *m_faceRec;
	BODYREC *m_torsoRec;

	CBodyDouble() {}
	CBodyDouble(unsigned int avID) : CBody(avID) {}
	CBodyDouble(const CBodyDouble &);
	virtual BOOL IsSame(CBody *other);
	virtual UCHAR GetClass() { return (UCHAR) BC_BODYDOUBLE; }
	virtual RECT DrawBody(CDC *dc, RECT &clientArea, BOOL drawNimbus);
	virtual void Draw(CDC *dc, POINT *ul, RECT *dmgRect);
	void GetBodyBox(CPose *head, CPose *body, RECT &clientRect, RECT &fullRect, RECT &headRect, RECT &torsoRect); 
	virtual CBody *Clone() { return (new CBodyDouble(*this)); }
	virtual void GetDimInfo(short &xdim, short &ydim, short &normHeight, short &headHeight, short &faceX);
	virtual void FlipBodyBox(RECT &fullBox, RECT &headBox, RECT &torsoBox);
//	virtual POINT GetFaceCenter();
};

class CBodySingle: public CBody {
public:
	RBODYREC *m_bodyRec;

	CBodySingle() {}
	CBodySingle(unsigned int avID) : CBody(avID) {}
	CBodySingle(const CBodySingle &);
	virtual BOOL IsSame(CBody *other);
	virtual UCHAR GetClass() { return (UCHAR) BC_BODYSINGLE; }
	virtual RECT DrawBody(CDC *dc, RECT &clientArea, BOOL drawNimbus);
	virtual void Draw(CDC *dc, POINT *ul, RECT *dmgRect);
	virtual void GetBodyBox(CPose *boy, RECT &clientRect, RECT &fullRect);
	virtual CBody *Clone() { return (new CBodySingle(*this)); }
	virtual void GetDimInfo(short &xdim, short &ydim, short &normHeight, short &headHeight, short &faceX);
	virtual void FlipBodyBox(RECT &fullBox);
	virtual short GetPoseID() { return (m_bodyRec->poseID); }
};

class CBodyUnary: public CBodySingle {
public:
	USHORT m_bodyID;

	CBodyUnary() {}
	CBodyUnary(unsigned int avID) : CBodySingle(avID) {}
	CBodyUnary(const CBodyUnary &);
	virtual CBody *Clone() { return (new CBodyUnary(*this)); }
	virtual short GetPoseID() { return (m_bodyID); }
};

#define AF_UNFROZEN		1		// Store these eventually in m_flags?
#define AF_TEMPFROZEN	2
#define AF_FROZEN		3

#define HEADMASK	1			// Avatar flags
#define TORSOMASK	2
#define TORSOFIRST	4
#define OTHERMAPPED	8

class CAvatarX {
public:
	USHORT m_avatarID;					// temporary: for identification
	USHORT m_origID;					// id of original from which this was dup'ed
	char *m_name;
	UCHAR m_style;
	UCHAR m_freeze;
	UCHAR m_flags;
	UCHAR m_lastDir;
	CBody *m_body;
	CWordArray m_talkTo;
	USHORT m_icon;					   // poseID for icon
	USHORT m_lastRight;				   // avatar last seen to the right (for positioning historesis)
	USHORT m_lastLeft;				   // avatar last seen to the left (for positioning historesis)
	USHORT m_nSends;
	USHORT m_nCopies;
	void *m_userInfo;				   // really a CUserInfo * (but not known to this module)

	CAvatarX() { Initialize(); }
	CAvatarX(const CAvatarX &);
	void Initialize() { m_freeze = AF_UNFROZEN; m_name = NULL; m_style = 0; m_body = NULL; m_userInfo = NULL; m_avatarID = m_origID = 0; m_flags = 0; m_nSends = 0; m_nCopies = 0; m_talkTo.SetSize(0,5); m_lastDir = FALSE; m_lastLeft = m_lastRight = 0;}
	virtual ~CAvatarX();
	virtual CBody *GetBodyFromEmotion(CEmotion &) = 0;
	virtual CBody *GetBodyFromEmotion(CEmotionOpts &) = 0;
	virtual void DifferentTorso(int torsoIndex) {}; // Dummy version by default
	virtual void SetNeutral() = 0;
	virtual void RecordBody(CBody*) = 0;
	virtual void UpdateBody(CBody*);
	virtual void SelectTalkTo(UINT avID, BOOL extend, BOOL updateMemberList = TRUE);
	virtual void DeselectTalkTo(UINT avID, BOOL updateMemberList = TRUE);
	virtual void ClearTalkTos();
	virtual void GetIndices(UCHAR &faceIndex, UCHAR &torsoIndex, UCHAR &requested) = 0;
	virtual void SetIndices(UCHAR faceIndex, UCHAR torsoIndex, UCHAR requested) = 0;
	virtual void GetEmotions(CEmotion &face, CEmotion &torso) = 0;
	virtual void SetEmotions(CEmotion &face, CEmotion &torso) = 0;
	void GetAvatarName(const char **name, const char **nickname);
	BOOL LoadBasics(int key, FILE *fp, const char *path);
	void LoadIconRec(FILE *fp, const char *path);
	virtual CAvatarX *DupAvatar() = 0;
	CAvatarX *IndexAvatar();
	const char *OriginalName();
	void SelectInMemberList(UINT avID, BOOL select = TRUE);
	void SelectMultipleInMemberList(UINT avID);
	void UnselectAll();
};


class CAvatarSimple : public CAvatarX {
public:
	RBODYREC *bRec;
	short m_nBodies;
	short m_lastBody;

	CAvatarSimple() { m_lastBody = -1; }
	CAvatarSimple(const CAvatarSimple &);
	void SetBody(CBodySingle *body, int tIndex) { body->m_bodyRec = bRec + tIndex; }
	virtual CBody *GetBodyFromEmotion(CEmotion &);
	virtual CBody *GetBodyFromEmotion(CEmotionOpts &);
	virtual ~CAvatarSimple();
	void GetBodyIndexFromEmotion(CEmotion &emotion, int& bIndex);
	virtual void SetNeutral();              // sets m_body to be only known body
	virtual void RecordBody(CBody *);		// don't bother saving state
	virtual void GetIndices(UCHAR &faceIndex, UCHAR &torsoIndex, UCHAR &requested);
	virtual void SetIndices(UCHAR faceIndex, UCHAR torsoIndex, UCHAR requested);
	virtual void GetEmotions(CEmotion &face, CEmotion &torso);
	virtual void SetEmotions(CEmotion &face, CEmotion &torso);
	static CAvatarX *LoadAvatar(FILE *fp, int avType, int version, const char *path);
	void LoadBodyRecs(FILE *fp, const char *path);
	void SetBodyNeutral(CBodySingle *body);
	virtual CAvatarX *DupAvatar() { m_nCopies++; return new CAvatarSimple(*this); };
};

class CAvatarChorus : public CAvatarSimple {
};


class CAvatarComplex : public CAvatarX {
public:
	FACEREC *fRec;
	BODYREC *bRec;
	short nFaces;
	short nTorsos;
	short m_lastFace;
	short m_lastTorso;

	CAvatarComplex() { m_lastFace = m_lastTorso = -1; }
	CAvatarComplex(const CAvatarComplex &);
	virtual ~CAvatarComplex();
	virtual CBody *GetBodyFromEmotion(CEmotion &);
	virtual CBody *GetBodyFromEmotion(CEmotionOpts &);
	void GetHeadAndBodyFromEmotion(CEmotion &emotion, int& fIndex, int& tIndex);
	virtual void DifferentTorso(int torsoIndex);
	void SetFace(CBodyDouble *body, int fIndex) { body->m_faceRec = fRec + fIndex; }
	void SetTorso(CBodyDouble *body, int tIndex) { body->m_torsoRec = bRec + tIndex; }
	void SetFaceNeutral(CBodyDouble *body);
	void SetTorsoNeutral(CBodyDouble *body);
	virtual void SetNeutral();
	virtual void RecordBody(CBody *);
	virtual void GetIndices(UCHAR &faceIndex, UCHAR &torsoIndex, UCHAR &requested);
	virtual void SetIndices(UCHAR faceIndex, UCHAR torsoIndex, UCHAR requested);
	virtual void GetEmotions(CEmotion &face, CEmotion &torso);
	virtual void SetEmotions(CEmotion &face, CEmotion &torso);
	static CAvatarX *LoadAvatar(FILE *fp, int avType, int version, const char *path);
	void LoadFaceRecs(FILE *fp, const char *path);
	void LoadTorsoRecs(FILE *fp, const char *path);
	virtual CAvatarX *DupAvatar() { m_nCopies++; return new CAvatarComplex(*this); };
};

#define NEMOTIONS		8

#define EM_HAPPY		((float)(0 * 2 * PI / 8))
#define EM_COY			((float)(1 * 2 * PI / 8))
#define EM_BORED		((float)(2 * 2 * PI / 8))
#define	EM_SCARED		((float)(3 * 2 * PI / 8))
#define EM_SAD			((float)(4 * 2 * PI / 8))
#define EM_ANGRY		((float)(5 * 2 * PI / 8))
#define EM_SHOUT		((float)(6 * 2 * PI / 8))
#define EM_LAUGH		((float)(7 * 2 * PI / 8))
#define EM_NEUTRAL		((float)0.0)


#define EM_WAVE			((float)1001.0)
#define EM_POINTOTHER	((float)1002.0)
#define EM_POINTSELF	((float)1003.0)
#define EM_DOUBLEPOINT	((float)1004.0)
#define EM_SHRUG		((float)1005.0)
#define EM_3QRWALK		((float)1006.0)
#define EM_SIDEWALK		((float)1007.0)
#define EM_3QFWALK		((float)1008.0)

#define AI_SUSAN		1
#define AI_TONGUETYED	2
#define AI_JORDAN		3
#define AI_LANCE		4
#define AI_MIKE			8

typedef struct {
	const char *filename;
	UINT fgndOffset;
	UINT transOffset;
	UINT auraOffset;
//	USHORT normHeight;
} AVFileRec;


CPose *GetPoseFromID(unsigned short poseID, BOOL loadMask = TRUE);
void InitializeAvatars();
void DestroyAvatars();
CAvatarX* GetAvatar(USHORT avatarID);
CAvatarX* GetAvatar(const char *);
CAvatarX* GetAvatar2(const char *);
CAvatarX* GetAvatar3(const char *, void *theirPui = NULL);
void GetNextAvatarName(char *);
CAvatarX* MyAvatar();
UINT MyAvatarID();
