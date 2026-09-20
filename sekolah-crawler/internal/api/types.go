package api

// CariSekolahRequest is the POST body for the school search/listing endpoint.
type CariSekolahRequest struct {
	Page             int    `json:"page"`
	Size             int    `json:"size"`
	Keyword          string `json:"keyword"`
	KabupatenKota    string `json:"kabupaten_kota"`
	BentukPendidikan string `json:"bentuk_pendidikan"`
	StatusSekolah    string `json:"status_sekolah"`
}

// CariSekolahItem is one row from the listing endpoint.
type CariSekolahItem struct {
	Provinsi         string `json:"provinsi"`
	Kabupaten        string `json:"kabupaten"`
	Kecamatan        string `json:"kecamatan"`
	Npsn             string `json:"npsn"`
	Nama             string `json:"nama"`
	BentukPendidikan string `json:"bentuk_pendidikan"`
	SekolahID        string `json:"sekolah_id"`
	StatusSekolah    string `json:"status_sekolah"`
}

type CariSekolahResponse struct {
	StatusCode int               `json:"status_code"`
	Message    string            `json:"message"`
	Total      int               `json:"total"`
	Data       []CariSekolahItem `json:"data"`
}

type BentukPendidikanItem struct {
	ID   int    `json:"bentuk_pendidikan_id"`
	Nama string `json:"nama"`
}

type BentukPendidikanResponse struct {
	StatusCode int                    `json:"status_code"`
	Message    string                 `json:"message"`
	Data       []BentukPendidikanItem `json:"data"`
}

// Sekolah is the core profile block returned by the full-detail endpoint.
type Sekolah struct {
	Provinsi             string   `json:"provinsi"`
	Akreditasi           *string  `json:"akreditasi"`
	AlamatJalan          string   `json:"alamat_jalan"`
	Kabupaten            string   `json:"kabupaten"`
	NamaDusun            *string  `json:"nama_dusun"`
	LuasTanahMilik       *float64 `json:"luas_tanah_milik"`
	LuasTanahBukanMilik  *float64 `json:"luas_tanah_bukan_milik"`
	DayaListrik          *float64 `json:"daya_listrik"`
	KodePos              *string  `json:"kode_pos"`
	Npsn                 string   `json:"npsn"`
	Nama                 string   `json:"nama"`
	NomorTelepon         *string  `json:"nomor_telepon"`
	BentukPendidikan     string   `json:"bentuk_pendidikan"`
	AksesInternet        *string  `json:"akses_internet"`
	SekolahID            string   `json:"sekolah_id"`
	Rt                   *int     `json:"rt"`
	Rw                   *int     `json:"rw"`
	Bujur                *float64 `json:"bujur"`
	SemesterID           *string  `json:"semester_id"`
	SumberListrik        *string  `json:"sumber_listrik"`
	AksesInternet2       *string  `json:"akses_internet_2"`
	WaktuPenyelenggaraan *string  `json:"waktu_penyelenggaraan"`
	Email                *string  `json:"email"`
	Website              *string  `json:"website"`
	Lintang              *float64 `json:"lintang"`
	Kecamatan            string   `json:"kecamatan"`
	YayasanID            *string  `json:"yayasan_id"`
	StatusSekolah        string   `json:"status_sekolah"`
	Yayasan              *string  `json:"yayasan"`
	SemesterKeterangan   *string  `json:"semester_keterangan"`
}

type Kurikulum struct {
	SemesterID string `json:"semester_id"`
	Kurikulum  string `json:"kurikulum"`
}

type Ptk struct {
	PtkGuruL *int `json:"ptk_guru_l"`
	PtkGuruP *int `json:"ptk_guru_p"`
}

type RasioSiswa struct {
	JmlPd            *int     `json:"jml_pd"`
	JmlRombel        *int     `json:"jml_rombel"`
	RasioSiswaRombel *float64 `json:"rasio_siswa_rombel"`
	JmlPdP           *int     `json:"jml_pd_p"`
	JmlPdL           *int     `json:"jml_pd_l"`
}

type RasioRombelRuangKelas struct {
	RasioRombelRuangKelas *float64 `json:"rasio_rombel_ruang_kelas"`
}

type RasioSiswaGuru struct {
	RasioSiswaGuru *float64 `json:"rasio_siswa_guru"`
}

type PersentaseGuru struct {
	PersentaseGuruKlasifikasi *float64 `json:"persentase_guru_klasifikasi"`
	PersentaseGuruSertifikasi *float64 `json:"persentase_guru_sertifikasi"`
	PersentaseGuruASN         *float64 `json:"persentase_guru_ASN"`
}

type PersentaseRuangKelasLayak struct {
	Rasio *float64 `json:"rasio"`
}

// FullDetailData is the "data" object from the full-detail endpoint.
// Ruang is kept as a generic map because its value set mixes numbers with
// a "sekolah_id" string key; the fixed field list is derived in schema.go.
//
// Sections not needed for the requested export (foto_sekolah, sekolah_sekitar,
// instansi_sekitar, daya_tampung, file_operasional) are intentionally not
// modeled here — they are still preserved verbatim when --raw-jsonl is used,
// since that writes the untouched response body.
type FullDetailData struct {
	Sekolah                   []Sekolah                   `json:"sekolah"`
	Ruang                     []map[string]any            `json:"ruang"`
	Ptk                       []Ptk                       `json:"ptk"`
	Kurikulum                 []Kurikulum                 `json:"kurikulum"`
	RasioSiswa                []RasioSiswa                `json:"rasio_siswa"`
	RasioRombelRuangKelas     []RasioRombelRuangKelas     `json:"rasio_rombel_ruang_kelas"`
	RasioSiswaGuru            []RasioSiswaGuru            `json:"rasio_siswa_guru"`
	PersentaseGuru            []PersentaseGuru            `json:"persentase_guru"`
	PersentaseRuangKelasLayak []PersentaseRuangKelasLayak `json:"persentase_ruang_kelas_layak"`
}

type FullDetailResponse struct {
	StatusCode int            `json:"status_code"`
	Message    string         `json:"message"`
	Data       FullDetailData `json:"data"`
}
