// dib.h : header file
//
// CDIB class
//

#ifndef __DIB__
#define __DIB__

class CDIB : public CObject
{
    DECLARE_SERIAL(CDIB)
public:
    CDIB();
    ~CDIB();

    BITMAPINFO* GetBitmapInfoAddress()
        {return m_pBMI;}                        // Pointer to bitmap info
    void* GetBitsAddress()
        {return m_pBits;}                       // Pointer to the bits
    RGBQUAD* GetClrTabAddress()
        {return (LPRGBQUAD)(((BYTE*)(m_pBMI)) 
            + sizeof(BITMAPINFOHEADER));}       // Pointer to color table
    int GetNumClrEntries();                     // Number of color table entries
    BOOL Create(int width, int height);         // Create a new DIB
    BOOL Create(BITMAPINFO* pBMI, BYTE* pBits); // Create from existing mem
    void* GetPixelAddress(int x, int y);
    virtual BOOL Load(CFile* fp);               // Load from file
	virtual BOOL Load(FILE *fp);				// Load from file
    virtual BOOL Load(const char* pszFileName = NULL);// Load DIB from disk file
    virtual BOOL Load(WORD wResid);             // Load DIB from resource
    virtual BOOL Save(const char* pszFileName = NULL);// Save DIB to disk file
    virtual BOOL Save(CFile* fp);               // Save to file
    virtual void Serialize(CArchive& ar);
    virtual void Draw(CDC* pDC, int x, int y);
	virtual void Draw(CDC* pDC,
					  int x, int y,
					  int destWidth, int destHeight,
					  int rop = SRCCOPY);
	virtual void Draw(CDC* pDC, int destX, int destY, int destWidth, int destHeight, 
					  int srcX, int srcY, int srcWidth, int srcHeight, int rop);
    virtual int GetWidth() {return DibWidth();}   // Image width
    virtual int GetHeight() {return DibHeight();} // Image height
    virtual BOOL MapColorsToPalette(CPalette* pPal);
    virtual void GetRect(CRect* pRect);
    virtual void CopyBits(CDIB* pDIB, 
                          int xd, int yd,
                          int w,  int h,
                          int xs, int ys,
                          COLORREF clrTrans = 0xFFFFFFFF);
	virtual void Convert8ToNonRLE();
	virtual void Convert4ToNonRLE();
	virtual void ConvertToNonRLE();


protected:
    BITMAPINFO* m_pBMI;         // Pointer to BITMAPINFO struct
    BYTE* m_pBits;              // Pointer to the bits
    BOOL  m_bMyBits;            // TRUE if DIB owns Bits memory

private:
    int DibWidth()
        {return m_pBMI->bmiHeader.biWidth;}
    int DibHeight() 
        {return m_pBMI->bmiHeader.biHeight;}
    int StorageWidth()
        {return (m_pBMI->bmiHeader.biWidth + 3) & ~3;}
};

#endif // __DIB__
